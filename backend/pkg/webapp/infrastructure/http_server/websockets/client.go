package websockets

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	websockets3 "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 1024 * 100

	// spaShutdownCloseCode is the close code the web UI sends when it shuts
	// its own socket down (sign-out, a socket that is not to be authorised):
	// an ordinary end, like a tab going away.
	spaShutdownCloseCode = 4242
)

// expectedCloseCodes are the ways a browser ordinarily ends the socket: a
// normal close, a tab or page going away, a close frame with no status, the
// UI's own shutdown code, and a connection dropped without a close frame.
var expectedCloseCodes = []int{
	websocket.CloseNormalClosure,
	websocket.CloseGoingAway,
	websocket.CloseNoStatusReceived,
	websocket.CloseAbnormalClosure,
	spaShutdownCloseCode,
}

// connectionAlreadyClosed reports an error that only says the socket was
// closed first — by the other pump, a revocation or the peer: writing to or
// closing a connection that is already gone. Every socket ends that way, so
// it is not an error worth an operator's attention.
func connectionAlreadyClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, websocket.ErrCloseSent)
}

// logEnd logs a failure on the way out of a pump: DEBUG when the socket was
// simply closed already, ERROR otherwise.
func (c *Client) logEnd(msg string, err error) {
	if connectionAlreadyClosed(err) {
		c.logger.Debug(msg, "error", err, "client_id", c.ClientID)
		return
	}
	c.logger.Error(msg, "error", err, "client_id", c.ClientID)
}

// Client is a middleman between the websocket connection and the hub.
type Client struct {
	conn *websocket.Conn

	fromClient chan websockets3.InboundMessage

	toClient chan websockets3.MessagePayload

	removeClientQueue chan websockets3.ClientID

	ClientID websockets3.ClientID

	// authorized flips once the client passes authorization; the pre-auth
	// reaper reads it so it cannot close a socket that authorized in the
	// meantime.
	authorized atomic.Bool

	// closed latches once the socket is finished — Close was called, the read
	// pump exited, or the hub removed the client. It is terminal: the manager
	// refuses to publish a closed client as authorized, because a client
	// re-entering a map after its removal event was consumed is never removed
	// again.
	closed atomic.Bool

	// dropped guards the close-on-overflow path in Send, so a burst that
	// overflows the buffer many times logs and closes once rather than per
	// message.
	dropped atomic.Bool

	// expiresAtUnixNano is the authorizing access token's expiry (0 until
	// authorized). Once set, WritePump closes the socket at that time so a
	// long-lived tab cannot outlive its token — bounding the WS revocation
	// window to the access-token TTL, matching the HTTP surface.
	expiresAtUnixNano atomic.Int64

	logger *slog.Logger
}

// SetExpiry bounds the client's lifetime to the authorizing token's expiry.
func (c *Client) SetExpiry(t time.Time) {
	c.expiresAtUnixNano.Store(t.UnixNano())
}

// expired reports whether the authorizing token has expired.
func (c *Client) expired() bool {
	exp := c.expiresAtUnixNano.Load()
	return exp != 0 && time.Now().UnixNano() > exp
}

// Close tears the socket down NOW, without waiting for the token to expire or
// for the next ping tick.
//
// Used when a membership is revoked: the expiry mechanism bounds a stale
// session to the access-token TTL, which is the right backstop but the wrong
// answer for "this person no longer works here". Closing the connection makes
// ReadPump fail, which enqueues the removal through the ordinary path — so the
// bookkeeping stays in one place.
// Nil-safe on purpose. Close is now also reached from Send, which runs on the
// hub's single fan-out goroutine — and that goroutine has no panic recovery, so
// a nil dereference there would take the whole process down rather than one
// connection. A client without a socket is nothing to close.
func (c *Client) Close() {
	c.markClosed()
	if c.conn == nil {
		return
	}
	if err := c.conn.Close(); err != nil {
		c.logger.Debug("closing a revoked websocket", "client_id", c.ClientID, "error", err)
	}
}

// Send queues a message for this client, or drops the client if it cannot keep
// up. It must never block.
//
// This used to be a bare `c.toClient <- m`, called serially from the hub's
// SINGLE fan-out goroutine. One browser that stopped reading — a laptop asleep
// with a tab open, a proxy holding the socket, a tab throttled to death by the
// browser — filled its 1000-slot buffer and then blocked that goroutine, which
// back-pressured into the shared toClients channel, whose senders include HTTP
// handlers, the gRPC metrics path and the workflow engine. One stalled client
// therefore froze live updates for every tenant, and then the platform behind
// them.
//
// Dropping is the right answer rather than growing the buffer: a client 1000
// messages behind is not going to catch up, and everything it missed is
// re-fetched on reconnect — the UI treats the socket as a change hint, not as a
// source of truth. Closing makes ReadPump fail, which enqueues removal through
// the ordinary path, so the bookkeeping stays in one place.
func (c *Client) Send(m websockets3.MessagePayload) {
	select {
	case c.toClient <- m:
	default:
		if c.dropped.CompareAndSwap(false, true) {
			c.logger.Warn("websocket client fell too far behind; closing it rather than stalling the hub",
				"client_id", c.ClientID, "buffered", cap(c.toClient))
			c.Close()
		}
	}
}

// Closed reports whether the socket is finished (see the closed field).
func (c *Client) Closed() bool { return c.closed.Load() }

// markClosed latches the terminal state; there is no way back.
func (c *Client) markClosed() { c.closed.Store(true) }

// Authorized reports whether this client ever completed authorization. Used by
// the pre-auth reaper, which must not close a socket that authorized while the
// timer was pending.
func (c *Client) Authorized() bool { return c.authorized.Load() }

// MarkAuthorized records that the client passed authorization.
func (c *Client) MarkAuthorized() { c.authorized.Store(true) }

// ReadPump reads messages from the WebSocket connection and sends them to the hub
func (c *Client) ReadPump() {
	defer func() {
		// The pump exiting means the socket is done; latch that BEFORE the
		// removal is enqueued, so an authorization racing the hub's removal
		// sees a closed client either way.
		c.markClosed()
		c.removeClientQueue <- c.ClientID
		if err := c.conn.Close(); err != nil {
			c.logEnd("error while closing websocket connection", err)
		}
		c.logger.Debug("client read pump stopped", "client_id", c.ClientID)
	}()

	c.conn.SetReadLimit(maxMessageSize)
	err := c.conn.SetReadDeadline(time.Now().Add(pongWait))
	if err != nil {
		c.logger.Error("error while setting read deadline", "error", err, "client_id", c.ClientID)
	}

	c.conn.SetPongHandler(func(string) error {
		errC := c.conn.SetReadDeadline(time.Now().Add(pongWait))
		if errC != nil {
			c.logger.Error("error while setting pong read deadline", "error", errC, "client_id", c.ClientID)
		}
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, expectedCloseCodes...) {
				c.logger.Error("connection closed unexpectedly", "error", err, "client_id", c.ClientID)
			} else {
				c.logger.Debug("client connection closed", "client_id", c.ClientID)
			}
			break
		}

		m := websockets3.InboundMessage{}
		err = json.Unmarshal(message, &m)
		if err != nil {
			c.logger.Error("error unmarshalling message", "error", err, "client_id", c.ClientID)
			// Send error response to client
			c.Send(websockets3.NewMessageError("invalid message format"))
			continue
		}

		m.ClientID = c.ClientID
		c.fromClient <- m
	}
}

func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		if err := c.conn.Close(); err != nil {
			c.logEnd("Error while closing websocket connection", err)
		}
	}()
	for {
		select {
		case message, ok := <-c.toClient:
			if message == nil {
				c.logger.Error("message is nil", "client_id", c.ClientID)
				continue
			}

			err := c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err != nil {
				c.logEnd("Error while setting write deadline", err)
			}
			if !ok {
				// The hub closed the channel.
				err = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				if err != nil {
					c.logEnd("Error while closing websocket connection", err)
				}
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			_, err = w.Write(message.ToBytes())
			if err != nil {
				c.logger.Error("Error while writing message", "error", err)
			}

			if err := w.Close(); err != nil {
				c.logEnd("Error while closing websocket connection", err)
				return
			}
		case <-ticker.C:
			// Close the socket once the authorizing token has expired; the
			// client reconnects and re-authorizes with a fresh token, which
			// re-checks membership/bindings.
			if c.expired() {
				c.logger.Info("closing websocket: authorizing token expired", "client_id", c.ClientID)
				return
			}
			err := c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err != nil {
				c.logEnd("Error while setting write deadline", err)
			}
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.logEnd("Error while writing ping", err)
				return
			}
		}
	}
}

func NewClient(conn *websocket.Conn, fromClient chan websockets3.InboundMessage, removeClientQueue chan websockets3.ClientID, logger *slog.Logger) Client {
	return Client{
		ClientID:          websockets3.NewClientID(),
		toClient:          make(chan websockets3.MessagePayload, 1000),
		conn:              conn,
		fromClient:        fromClient,
		removeClientQueue: removeClientQueue,
		logger:            logger,
	}
}
