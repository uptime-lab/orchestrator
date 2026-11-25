package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// One writer goroutine per connection, fed by a small buffered channel. It
// holds two properties that share a cause.
//
// grpc-go forbids concurrent SendMsg on one stream; doing it anyway is undefined
// and in practice takes the process down. There are two independent producers:
// the command dispatch loop and the HTTP/WebSocket terminal handlers. The trigger —
// opening a web terminal on a node while a command is dispatched to it — is
// ordinary during debugging, which is exactly when you least want the control
// plane to die. (Reads of the connection registry are synchronised separately,
// by connectionsSnapshot.)
//
// The second property is about latency, not safety. Fleet-wide dispatch in
// exactly ONE goroutine doing a blocking Stream.Send would let a single agent
// that has stopped reading its stream stall commands for EVERY node until
// keepalive reaps it. With a writer per connection, a wedged node blocks only its own
// writer; the dispatcher hands the frame over and moves on. The WebSocket hub
// has the same shape.
const (
	// outboundBuffer is how many frames may wait for one node. Comfortably
	// larger than maxRedeliverBatch (20), the biggest legitimate burst, so a
	// reconnecting node's queued commands never trip the full-queue path.
	outboundBuffer = 64

	// enqueueGrace is how long an enqueue waits when that buffer is full. Not
	// zero, so a node that is merely a moment behind is not failed; short,
	// because a full buffer means the agent is not reading and the whole point
	// is that one such node must not hold up the fleet.
	enqueueGrace = 500 * time.Millisecond
)

// startWriter launches this connection's writer. It must be called exactly once,
// before the connection is published in the registry.
func (c *StreamConnection) startWriter(ctx context.Context, logger *slog.Logger) {
	go func() {
		defer close(c.writerDone)
		for {
			select {
			case <-ctx.Done():
				c.setSendErr(fmt.Errorf("connection to node %s closed", c.NodeID))
				return
			case <-c.quit:
				c.setSendErr(fmt.Errorf("connection to node %s closed", c.NodeID))
				return
			case msg := <-c.out:
				if err := c.Stream.Send(msg); err != nil {
					// One failed Send means the stream is finished: grpc-go
					// returns the same error for every later call, so retrying
					// here would only spin. Record it and stop, which makes
					// every subsequent enqueue fail fast with this reason
					// instead of filling a buffer nobody will drain.
					c.setSendErr(err)
					logger.Warn("outbound stream writer stopped", "node_id", c.NodeID, "error", err)
					return
				}
			}
		}
	}()
}

// Send queues one platform→node frame for this connection's writer.
//
// It is the ONLY way to write to a registered connection's stream. Anything that
// calls Stream.Send directly reintroduces the concurrent-writer bug, and the
// compiler cannot stop it — so the rule lives here and in the field comment.
func (c *StreamConnection) Send(msg *proto.PlatformToNode) error {
	if msg == nil {
		return fmt.Errorf("refusing to send a nil frame to node %s", c.NodeID)
	}
	if c.out == nil {
		// A connection built without a writer (only tests do this). Failing is
		// better than a nil-channel block that never returns.
		return fmt.Errorf("node %s has no outbound writer", c.NodeID)
	}

	// Fail fast if the writer has already given up, rather than parking a frame
	// in a buffer nothing will drain.
	select {
	case <-c.writerDone:
		return c.sendError()
	default:
	}

	timer := time.NewTimer(enqueueGrace)
	defer timer.Stop()
	select {
	case c.out <- msg:
		return nil
	case <-c.writerDone:
		return c.sendError()
	case <-timer.C:
		return fmt.Errorf("node %s is not reading its stream: %d frames already queued", c.NodeID, cap(c.out))
	}
}

// Stop ends the writer. Idempotent, because both the stream teardown and the
// monitor's stale-connection cleanup may reach the same connection.
//
// The outbound channel is deliberately NEVER closed: a sender racing a close
// would panic, and there is no lock ordering that removes that race while
// senders are lock-free. Signalling on a separate channel costs nothing and
// cannot panic.
func (c *StreamConnection) Stop() {
	if c.quit == nil {
		return
	}
	c.stopOnce.Do(func() { close(c.quit) })
}

func (c *StreamConnection) setSendErr(err error) {
	c.sendErrMu.Lock()
	if c.sendErr == nil {
		c.sendErr = err
	}
	c.sendErrMu.Unlock()
}

// sendError is why the writer stopped, for a caller whose enqueue arrived too
// late. It never returns nil, so a failed Send always carries a reason.
func (c *StreamConnection) sendError() error {
	c.sendErrMu.Lock()
	defer c.sendErrMu.Unlock()
	if c.sendErr != nil {
		return c.sendErr
	}
	return fmt.Errorf("connection to node %s is closed", c.NodeID)
}

// waitForWriter is used by tests to observe a stopped writer without sleeping.
func (c *StreamConnection) waitForWriter(timeout time.Duration) bool {
	if c.writerDone == nil {
		return true
	}
	select {
	case <-c.writerDone:
		return true
	case <-time.After(timeout):
		return false
	}
}
