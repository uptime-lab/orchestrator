package terminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	audit_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/wsorigin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"golang.org/x/time/rate"
)

// Handler exposes a per-session WebSocket that bridges a browser xterm.js to a
// device PTY over the agent's existing gRPC stream. It is separate from the
// account fan-out /ws socket.
//
// SECURITY: this is a remote-shell capability. It is gated by three checks —
// a server kill-switch (config.Terminal.Enabled, default off), JWT auth on the
// WS handshake, and an authorization check that the caller's account owns the
// target node. Every session open and close is written to the AUDIT TRAIL, never
// the keystrokes.
//
// A `logger.Info` line alone would not hold that claim: it rotates out of the
// container log, and the most privileged thing in the product, an interactive
// root shell on managed hardware, must not be the one action with no durable
// record.
type Handler struct {
	terminalService services.TerminalService
	nodeService     services.NodeService
	authService     services.AuthService
	enforcer        *authz_service.Enforcer
	audit           *audit_service.Recorder
	cfg             *config.Config
	// twoFactorPolicy refuses a shell to a member who still owes an enrollment.
	twoFactorPolicy TwoFactorPolicy
	logger          *slog.Logger
	upgrader        websocket.Upgrader

	// sessionsMu/sessionsByUser count OPEN terminal sessions per user, for the
	// concurrency cap. In-memory on purpose, like the gRPC per-node stream
	// cap: a session lives and dies with this process, so a restart correctly
	// resets the count to the sessions that reconnect.
	sessionsMu     sync.Mutex
	sessionsByUser map[uuid.UUID]int

	// pendingMu/pending count sockets that have been UPGRADED but not yet
	// authenticated — the number a stranger controls, and the only bound the
	// handler had none of: the per-user cap above is keyed on a user id the
	// caller has not presented yet. pendingByIP is the same count per trusted
	// client address, so one source can hold only its own share of the pool
	// — an entry is deleted the moment it reaches zero, or the
	// map would grow by one key per address that ever tried. The limits come
	// from config.Terminal; maxPendingHandshakes is an older test override of
	// the global one, 0 = use the config.
	pendingMu            sync.Mutex
	pending              int
	pendingByIP          map[string]int
	maxPendingHandshakes int
}

// Arrival-rate bound on the handshake route, per trusted client address,
// applied as route middleware so it runs BEFORE the upgrade and before any
// counter is touched. The pending cap bounds sockets that STAY open; a source
// churning short-lived connections — connect, get refused or time out, connect
// again — holds no slot for long and needs a bound on arrivals instead. An
// operator opens a handful of terminals, never dozens per second: ten in a
// burst covers a browser reopening every pane at once, five per second
// sustained is far above any human pace. Same shape as the adoption and
// netboot limiters (Echo memory store keyed on c.RealIP(), which the server's
// IPExtractor makes trustworthy).
const (
	handshakeArrivalRate    = 5
	handshakeArrivalBurst   = 10
	handshakeArrivalExpires = 3 * time.Minute
)

// pendingRefusal says which bound refused a pre-auth slot.
type pendingRefusal int

const (
	pendingAdmitted pendingRefusal = iota
	pendingRefusedPerSource
	pendingRefusedGlobal
)

// pendingLimits resolves the two caps: the test override, then config, then
// the package defaults — never 0, because "unlimited" is the vulnerability.
func (h *Handler) pendingLimits() (global, perSource int) {
	global = h.maxPendingHandshakes
	if global <= 0 && h.cfg != nil {
		global = h.cfg.Terminal.MaxPendingHandshakes
	}
	if global <= 0 {
		global = config.DefaultTerminalMaxPendingHandshakes
	}
	if h.cfg != nil {
		perSource = h.cfg.Terminal.MaxPendingHandshakesPerIP
	}
	if perSource <= 0 {
		perSource = config.DefaultTerminalMaxPendingHandshakesPerIP
	}
	return global, perSource
}

// pendingFor reports the pending-handshake count for one source and whether
// the source has an entry at all; a test's view of the map.
func (h *Handler) pendingFor(source string) (int, bool) {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	n, ok := h.pendingByIP[source]
	return n, ok
}

// Bounds on what an unauthenticated or an authenticated peer may send.
//
// authFrameReadLimit bounds the first frame, which carries a JWT and two
// integers: a few hundred bytes. Before it, ReadJSON on the just-upgraded
// socket read an unbounded message into memory for a caller who had presented
// nothing.
//
// inputFrameReadLimit bounds every later frame. Each one is forwarded to the
// device as ONE TerminalInput on the node's platform→node gRPC stream, and an
// oversized gRPC frame fails Stream.Send, which the stream writer treats as
// terminal — the node's whole stream, not just the shell. The largest
// legitimate frame is a paste; the browser chunks pastes well under this.
//
// The bound on sockets between upgrade and auth — global and per source — is
// config.Terminal.MaxPendingHandshakes / MaxPendingHandshakesPerIP, with the
// config package's Default* constants as the floor; a real client
// authenticates within milliseconds.
const (
	authFrameReadLimit  = 8 * 1024
	inputFrameReadLimit = 64 * 1024
)

// defaultAuthFrameDeadline is how long the socket may stay silent before the
// auth frame. A test shrinks it through authFrameDeadlineOverride, which is
// atomic on purpose: an upgraded socket is HIJACKED, so its handler goroutine
// outlives httptest.Server.Close, and a pending handshake left by one test was
// still reading the plain var while the next test wrote it (race
// detector). 0 = the default.
const defaultAuthFrameDeadline = 10 * time.Second

var authFrameDeadlineOverride atomic.Int64

func authFrameDeadline() time.Duration {
	if d := authFrameDeadlineOverride.Load(); d > 0 {
		return time.Duration(d)
	}
	return defaultAuthFrameDeadline
}

// acquirePendingSlot counts an upgraded, not-yet-authenticated socket from
// one source in, refusing past either cap. Returns the release func (nil
// when refused) and which bound refused; release is idempotent, because it
// runs both on the pre-auth exit paths and on auth success, where the
// per-user session cap takes over. One release hands back BOTH counts under
// one lock, so the two can never disagree.
//
// The per-source cap is asked FIRST. It is the verdict the caller can act on
// — "this address has too many handshakes open" is about the caller — while
// the global 503 says the platform is busy, which is what everyone else would
// read while one source floods. Asked in this order a flooding source hears
// 429 about itself, the global 503 is reserved for a genuinely distributed
// load, and a per-source refusal never has to touch the global counter.
func (h *Handler) acquirePendingSlot(source string) (func(), pendingRefusal) {
	global, perSource := h.pendingLimits()
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	if h.pendingByIP == nil {
		h.pendingByIP = make(map[string]int)
	}
	if h.pendingByIP[source] >= perSource {
		return nil, pendingRefusedPerSource
	}
	if h.pending >= global {
		return nil, pendingRefusedGlobal
	}
	h.pending++
	h.pendingByIP[source]++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.pendingMu.Lock()
			defer h.pendingMu.Unlock()
			h.pending--
			if h.pendingByIP[source] <= 1 {
				delete(h.pendingByIP, source)
			} else {
				h.pendingByIP[source]--
			}
		})
	}, pendingAdmitted
}

// acquireSessionSlot counts the user in, refusing past the cap. Returns the
// release func (nil when refused). Refuse-not-evict is deliberate — see the
// config field: killing an operator's live root shell mid-command is worse
// than telling them to close one.
func (h *Handler) acquireSessionSlot(userID uuid.UUID) func() {
	limit := 0
	if h.cfg != nil {
		limit = h.cfg.Terminal.MaxSessionsPerUser
	}
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	if h.sessionsByUser == nil {
		h.sessionsByUser = make(map[uuid.UUID]int)
	}
	if limit > 0 && h.sessionsByUser[userID] >= limit {
		return nil
	}
	h.sessionsByUser[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.sessionsMu.Lock()
			defer h.sessionsMu.Unlock()
			if h.sessionsByUser[userID] <= 1 {
				delete(h.sessionsByUser, userID)
			} else {
				h.sessionsByUser[userID]--
			}
		})
	}
}

func NewHandler(
	terminalService services.TerminalService,
	nodeService services.NodeService,
	authService services.AuthService,
	enforcer *authz_service.Enforcer,
	auditRecorder *audit_service.Recorder,
	cfg *config.Config,
	logger *slog.Logger,
) *Handler {
	if auditRecorder == nil {
		panic("terminal.Handler requires an audit recorder")
	}
	return &Handler{
		terminalService: terminalService,
		nodeService:     nodeService,
		authService:     authService,
		enforcer:        enforcer,
		audit:           auditRecorder,
		cfg:             cfg,
		logger:          logger.With("handler", "terminal"),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Shared with the account fan-out socket so the two cannot drift
			// into different fail-open shapes again — this one used to allow
			// any origin whenever the configured list was empty.
			// Not exploitable as it stood, because authorization is an explicit
			// first-message JWT rather than an ambient cookie, but that is a
			// property of today's auth and not of the check.
			CheckOrigin: wsorigin.New(cfg, logger),
		},
	}
}

// Register wires the terminal WS route behind the per-source arrival limiter.
// Documented in Swagger as a WS upgrade.
func (h *Handler) Register(e *echo.Echo) {
	limiter := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(handshakeArrivalRate),
			Burst:     handshakeArrivalBurst,
			ExpiresIn: handshakeArrivalExpires,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return results.SendError(c, http.StatusTooManyRequests, "terminal handshake rate limit exceeded for this address", nil)
		},
	})
	e.GET("/api/v1/nodes/:id/terminal", h.handle, limiter)
}

// clientAuth is the first message the browser must send after connecting.
type clientAuth struct {
	Token string `json:"token"`
	Cols  uint32 `json:"cols"`
	Rows  uint32 `json:"rows"`
}

// clientInput is a subsequent message: keystrokes or a resize.
type clientInput struct {
	Type string `json:"type"` // "input" | "resize"
	Data string `json:"data"` // input bytes (UTF-8) for type=="input"
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

// handle upgrades the connection, authenticates via the first message, and
// bridges the browser to the device PTY.
//
// @Summary Open a device terminal (WebSocket)
// @Description Upgrades to a WebSocket bridging a browser terminal to the device shell over the agent stream. Gated by TERMINAL_ENABLED and account ownership. Auth via a first `{token}` message.
// @Tags nodes
// @Security ApiKeyAuth
// @Param id path string true "Node ID"
// @Success 101 "Switching Protocols"
// @Failure 403 {object} results.APIResponse "Terminal disabled by server policy"
// @Failure 429 {object} results.APIResponse "Too many terminal handshakes from this address, or the per-address handshake rate limit is exceeded"
// @Failure 503 {object} results.APIResponse "Too many terminal handshakes in progress"
// @Router /api/v1/nodes/{id}/terminal [get]
func (h *Handler) handle(c echo.Context) error {
	nodeID := c.Param("id")

	// Kill-switch first, and BEFORE the upgrade: a disabled terminal answers
	// with an HTTP status, not with a socket that then says no. Checked per
	// request rather than at registration so the route's documentation test
	// can force the family on.
	if !h.cfg.Terminal.Enabled {
		return results.SendForbiddenError(c, "terminal is disabled by server policy")
	}

	// Pre-auth bounds, also before the upgrade: an HTTP refusal costs nothing,
	// an upgraded socket costs a goroutine and a read buffer per stranger.
	// Keyed on the trusted client address (the server's IPExtractor honours
	// X-Forwarded-For from the proxy only), so a refusal names the source's
	// own quota and another address is unaffected by it.
	source := c.RealIP()
	releasePending, refused := h.acquirePendingSlot(source)
	switch refused {
	case pendingRefusedPerSource:
		h.logger.Warn("refusing terminal upgrade: too many handshakes in progress from one address", "source", source)
		return results.SendError(c, http.StatusTooManyRequests, "too many terminal handshakes from this address", nil)
	case pendingRefusedGlobal:
		h.logger.Warn("refusing terminal upgrade: too many handshakes in progress", "source", source)
		return results.SendError(c, http.StatusServiceUnavailable, "too many terminal handshakes in progress", nil)
	}
	defer releasePending()

	conn, err := h.upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		h.logger.Error("terminal ws upgrade failed", "error", err)
		return nil
	}
	defer conn.Close()

	// First message must carry the JWT (browsers can't set WS auth headers).
	// It is bounded before it is read: gorilla answers an overflow with close
	// 1009 and ErrReadLimit, which lands on the same path as a silent peer.
	conn.SetReadLimit(authFrameReadLimit)
	conn.SetReadDeadline(time.Now().Add(authFrameDeadline()))
	var auth clientAuth
	if err := conn.ReadJSON(&auth); err != nil {
		if errors.Is(err, websocket.ErrReadLimit) {
			lingerAfterCloseFrame(conn)
			return nil
		}
		writeFatal(conn, "expected auth message")
		return nil
	}
	conn.SetReadDeadline(time.Time{})

	claims, err := h.authService.ValidateAccessToken(auth.Token)
	if err != nil {
		writeFatal(conn, "invalid token")
		return nil
	}
	// The token verifies statelessly until its TTL; the session it names may
	// already be signed out. A family that is not live must not open a shell.
	if err := h.authService.RequireLiveSession(c.Request().Context(), claims.UserID, sessionProof(claims)); err != nil {
		writeFatal(conn, "session is no longer valid")
		return nil
	}

	// The enrollment gate that guards the REST surface cannot see this
	// handshake — auth is a first-frame token, not an HTTP middleware — so a
	// root shell on managed hardware would otherwise be the one thing reachable
	// without enrolling the second factor the account demands.
	if h.twoFactorPolicy != nil {
		ctx := c.Request().Context()
		if h.twoFactorPolicy.AccountRequiresTwoFactor(ctx, claims.AccountID) &&
			!h.twoFactorPolicy.UserHasTwoFactor(ctx, claims.UserID) {
			writeFatal(conn, "two_factor_enrollment_required")
			return nil
		}
	}

	// Authorization: the account must own the node.
	nodeUUID, err := uuid.Parse(nodeID)
	if err != nil {
		writeFatal(conn, "invalid node id")
		return nil
	}
	node, err := h.nodeService.GetByIDAndAccount(c.Request().Context(), nodeUUID, claims.AccountID)
	if err != nil {
		writeFatal(conn, "node not found or not authorized")
		return nil
	}
	// A root shell is admin+ within the node's OU scope; also re-checks that
	// the caller is still an active member (the handshake bypasses the HTTP
	// authz middleware, so it must resolve permissions itself).
	if err := h.enforcer.Authorize(c.Request().Context(), claims.AccountID, claims.UserID, authz.ActionNodeTerminal, node.OUID); err != nil {
		writeFatal(conn, "not authorized to open a terminal on this node")
		return nil
	}
	// The watcher agent carries no PTY handler: an open would
	// only hang until the dial timeout. In recovery the FULL agent runs and
	// re-reports its kind, so this clears itself exactly when a terminal
	// becomes possible.
	if node.IsWatcher() {
		writeFatal(conn, "this node runs the watcher agent, which provides no terminal; netboot it into recovery for a shell")
		return nil
	}

	// Concurrency cap per user, taken AFTER every auth check so an
	// unauthenticated caller cannot consume slots, and BEFORE the device is
	// asked to spawn a PTY.
	release := h.acquireSessionSlot(claims.UserID)
	if release == nil {
		writeFatal(conn, "too many open terminal sessions; close one and retry")
		return nil
	}
	defer release()
	// Authenticated and counted per user: the pre-auth slot is no longer the
	// bound that applies, so hand it back to the strangers' pool now rather
	// than holding it for the life of the shell.
	releasePending()

	session, err := h.terminalService.OpenTerminal(nodeID, auth.Cols, auth.Rows)
	if err != nil {
		writeFatal(conn, "failed to open terminal: node may be offline")
		return nil
	}

	// AUDIT: session open and close. See auditSession.
	defer h.auditSession(c.Request().Context(),
		audit_service.UserActor(claims.AccountID, claims.UserID),
		nodeID, session.SessionID())()

	// Re-authorize periodically: the handshake checks are one-shot, so
	// bridge() re-runs them on a timer and closes on denial. See reauthorize
	// for what "them" has to mean.
	ctx := c.Request().Context()
	h.bridge(conn, session, func() error { return h.reauthorize(ctx, claims, nodeUUID) })
	return nil
}

// sessionProof is what the access token says about the session it belongs to.
func sessionProof(claims *services.TokenClaims) services.SessionProof {
	proof := services.SessionProof{FamilyID: claims.FamilyID, CredentialGeneration: claims.CredentialGeneration}
	if claims.IssuedAt != nil {
		proof.IssuedAt = claims.IssuedAt.Time
	}
	return proof
}

// reauthorize is the periodic re-check behind an open shell, and it re-asks
// every question the handshake asked EXCEPT whether the access token has
// expired — that would end every shell at the token TTL while the browser is
// still refreshing.
//
// It re-reads the node rather than trusting the unit captured at the
// handshake: a node moved to another unit kept a shell open for a member who
// had never been granted that unit, and a deleted node's shell was ended only
// by the stream teardown. It then asks the enforcer against the CURRENT unit
// (membership, role, disabled member — the enforcer fails closed on a read
// error), and finally whether the session the token names is still live: a
// sign-out, "sign out everywhere" or a password change revokes the refresh
// family, and nothing else on this path reads that.
func (h *Handler) reauthorize(ctx context.Context, claims *services.TokenClaims, nodeUUID uuid.UUID) error {
	current, err := h.nodeService.GetByIDAndAccount(ctx, nodeUUID, claims.AccountID)
	if err != nil {
		return fmt.Errorf("node no longer reachable by this account: %w", err)
	}
	if err := h.enforcer.Authorize(ctx, claims.AccountID, claims.UserID, authz.ActionNodeTerminal, current.OUID); err != nil {
		return err
	}
	return h.authService.RequireLiveSession(ctx, claims.UserID, sessionProof(claims))
}

// auditSession records the opening of an interactive shell and returns the
// function that records its close — so the two rows are written by one piece of
// code and cannot drift apart.
//
// The PAIR is the point. An open row alone cannot distinguish a shell used for
// ten seconds from one left open for a day, and "how long did somebody have root
// on this device" is the question a shell record exists to answer. The session id
// joins the two rows. Keystrokes are never recorded.
func (h *Handler) auditSession(ctx context.Context, actor audit_service.Actor, nodeID, sessionID string) func() {
	detail := "session " + sessionID
	h.audit.RecordDetail(ctx, actor,
		audit_event.ActionNodeTerminalOpen, audit_event.TargetNode, nodeID, detail)
	h.logger.InfoContext(ctx, "terminal session opened",
		"user_id", actor.UserID, "account_id", actor.AccountID,
		"node_id", nodeID, "session", sessionID)

	return func() {
		// context.Background(), NOT the caller's ctx: this runs from a defer as the
		// WebSocket tears down, by which point the request context is already
		// cancelled. Using it would make the close row fail to write every single
		// time, leaving a trail of opens that never end — which reads as "somebody
		// still has a root shell on this device".
		h.audit.RecordDetail(context.Background(), actor,
			audit_event.ActionNodeTerminalClose, audit_event.TargetNode, nodeID, detail)
		h.logger.Info("terminal session ended",
			"user_id", actor.UserID, "node_id", nodeID, "session", sessionID)
	}
}

// terminalReauthInterval is how often an open terminal re-checks the caller's
// membership + node.terminal permission.
const terminalReauthInterval = 60 * time.Second

// Liveness for the terminal socket, mirroring the hub's values in
// infrastructure/http_server/websockets/client.go so both sockets behave the
// same way.
//
// Without these the read deadline was cleared after the auth frame and never
// set again, and nothing ever pinged — so an interactive session held open by a
// browser that went away (a slept laptop, a killed tab, a dropped VPN) stayed
// open until some TCP layer got around to it. What is on the other end of that
// session is a ROOT SHELL on the managed device, and the re-authorization
// ticker above does not help: it only asks whether the user is still allowed,
// never whether the user is still there.
//
// terminalPongWait is deliberately generous. An idle shell is a normal thing to
// have open, and the ping/pong keeps it alive without any traffic; the deadline
// only fires when the peer has genuinely stopped answering.
const (
	terminalWriteWait  = 10 * time.Second
	terminalPongWait   = 60 * time.Second
	terminalPingPeriod = (terminalPongWait * 9) / 10
)

// bridge pumps bytes both ways until either side closes.
func (h *Handler) bridge(conn *websocket.Conn, session services.TerminalSession, reauthorize func() error) {
	var writeMu sync.Mutex
	done := make(chan struct{})
	var closeOnce sync.Once
	stop := func() { closeOnce.Do(func() { close(done) }) }

	// write serializes every frame AND bounds it. A write with no deadline on a
	// peer that has stopped reading blocks forever, holding this goroutine and
	// the mutex, which would wedge the reader that shares it.
	write := func(messageType int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := conn.SetWriteDeadline(time.Now().Add(terminalWriteWait)); err != nil {
			return err
		}
		return conn.WriteMessage(messageType, data)
	}

	// Liveness. The browser→device loop below is a blocking ReadJSON, so the
	// read deadline is what actually ends a session whose peer vanished; the
	// pong handler pushes it forward for as long as the peer answers.
	_ = conn.SetReadDeadline(time.Now().Add(terminalPongWait))
	// An input frame becomes one gRPC message on the node's stream, and an
	// oversized one would kill that stream rather than this session. Overflow
	// closes the socket (1009), which ends the shell below.
	conn.SetReadLimit(inputFrameReadLimit)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(terminalPongWait))
	})

	go func() {
		ticker := time.NewTicker(terminalPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if err := write(websocket.PingMessage, nil); err != nil {
					stop()
					return
				}
			}
		}
	}()

	// Periodic re-authorization: stop the session if the caller loses access.
	go func() {
		ticker := time.NewTicker(terminalReauthInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if reauthorize() != nil {
					h.logger.Info("closing terminal: caller no longer authorized")
					stop()
					return
				}
			}
		}
	}()

	// Device → browser (binary frames) + session-end.
	go func() {
		for {
			select {
			case <-done:
				return
			case data, ok := <-session.Output():
				if !ok {
					stop()
					return
				}
				if err := write(websocket.BinaryMessage, data); err != nil {
					stop()
					return
				}
			case exit := <-session.Closed():
				_ = write(websocket.TextMessage, closeFrame(exit))
				stop()
				return
			}
		}
	}()

	// Browser → device.
	var tooBig atomic.Bool
	go func() {
		for {
			var in clientInput
			if err := conn.ReadJSON(&in); err != nil {
				tooBig.Store(errors.Is(err, websocket.ErrReadLimit))
				stop()
				return
			}
			switch in.Type {
			case "resize":
				h.terminalService.SendTerminalInput(session.SessionID(), nil, true, in.Cols, in.Rows)
			default:
				h.terminalService.SendTerminalInput(session.SessionID(), []byte(in.Data), false, 0, 0)
			}
		}
	}()

	<-done
	h.terminalService.CloseTerminal(session.SessionID())
	if tooBig.Load() {
		lingerAfterCloseFrame(conn)
	}
}

// closeLingerWait bounds lingerAfterCloseFrame. Only an oversized frame ever
// pays it, and the pending cap bounds how many strangers can at once.
const closeLingerWait = time.Second

// lingerAfterCloseFrame drains what the peer still had in flight, bounded, so
// the close frame gorilla already sent (1009 on a read-limit overflow) reaches
// it. Closing a socket with unread bytes in the kernel buffer answers with a
// TCP RST, which discards that close frame on its way out — the browser then
// sees an abnormal closure (1006) instead of the reason, and the reason is
// the one thing the client needs to know not to retry the same frame.
func lingerAfterCloseFrame(conn *websocket.Conn) {
	raw := conn.NetConn()
	_ = raw.SetReadDeadline(time.Now().Add(closeLingerWait))
	_, _ = io.Copy(io.Discard, raw)
}

func writeFatal(conn *websocket.Conn, msg string) {
	_ = conn.WriteMessage(websocket.TextMessage, mustJSON(map[string]string{"type": "error", "message": msg}))
}

func closeFrame(exit services.TerminalExit) []byte {
	return mustJSON(map[string]any{"type": "closed", "exit_code": exit.Code, "error": exit.Error})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// TwoFactorPolicy answers whether a member still owes an enrollment.
type TwoFactorPolicy interface {
	AccountRequiresTwoFactor(ctx context.Context, accountID uuid.UUID) bool
	UserHasTwoFactor(ctx context.Context, userID uuid.UUID) bool
}

// SetTwoFactorPolicy wires the enrollment requirement into the handshake.
func (h *Handler) SetTwoFactorPolicy(p TwoFactorPolicy) {
	h.twoFactorPolicy = p
}
