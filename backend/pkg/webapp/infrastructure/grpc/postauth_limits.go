package grpc

import (
	"sync"

	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
	"golang.org/x/time/rate"
)

// Bounds on what an AUTHENTICATED peer can make this process do.
//
// preauth_limits.go bounds the window before a token is validated. Without
// these bounds, one valid node credential — a shell on one device is enough —
// could open streams without limit and drive the message loop as fast as the
// network allows, and every frame in that loop does
// database work on behalf of the whole instance.
//
// Both bounds here are per NODE, never per account. The account is the tenancy
// unit for the HTTP API, but a fleet-wide bound is the one shape that can take
// a whole account offline at once, and the thing being bounded is a single
// compromised device.

const (
	// defaultMaxStreamsPerNode is the fallback when nothing is configured.
	//
	// An honest agent holds exactly ONE stream. It can transiently hold two —
	// it reconnects on a fresh stream while the previous one is still being
	// torn down — and a stream whose peer has vanished is retired by the
	// transport keepalive (30 s ping + 10 s timeout) rather than lingering. So
	// the honest ceiling is two, briefly, and 32 is sixteen times that: it
	// cannot be reached by reconnecting, only by a peer deliberately holding
	// streams open.
	defaultMaxStreamsPerNode = 32

	// defaultStreamMessagesPerSecond / defaultStreamBurst pace ONE stream's
	// inbound frames.
	//
	// Measured against what an agent actually sends: a heartbeat every 30 s, a
	// metrics sample every 5 s, command progress on a 15-second ticker. That is
	// well under one frame per second in steady state, and the largest real
	// burst is a reconnect replaying queued command results (maxRedeliverBatch
	// is 20). 20/s with a burst of 100 is two orders of magnitude above the
	// steady state and five times the largest burst.
	defaultStreamMessagesPerSecond = 20
	defaultStreamBurst             = 100
)

// nodeStreamGate counts authenticated streams per node.
//
// A counter rather than a semaphore channel, for the same reason preAuthGate is
// one: a refused acquire must return immediately, because blocking here holds
// the goroutine this exists to bound.
type nodeStreamGate struct {
	max int

	mu      sync.Mutex
	perNode map[string]int
}

// newNodeStreamGate builds the gate. A non-positive max disables it entirely
// (0 = unlimited, the repo's quota convention).
func newNodeStreamGate(max int) *nodeStreamGate {
	return &nodeStreamGate{max: max, perNode: make(map[string]int)}
}

// acquire takes a slot for nodeID. It reports the resulting in-flight count so
// a refusal can say how deep it is, and a release func that is safe to call
// more than once.
//
// A refusal returns ok=false and changes nothing. It refuses the NEW stream
// rather than displacing an old one because displacing is not enforceable:
// cancelling a stream's context does not unblock its stream.Recv, so a peer
// that opens a stream and then says nothing cannot be made to go away from
// here. Refusal is self-healing — the peer's own backoff retries, and by then
// the keepalive has reaped whatever was wedged.
func (g *nodeStreamGate) acquire(nodeID string) (release func(), inFlight int, ok bool) {
	if g == nil || g.max <= 0 {
		return func() {}, 0, true
	}

	g.mu.Lock()
	n := g.perNode[nodeID]
	if n >= g.max {
		g.mu.Unlock()
		return func() {}, n, false
	}
	n++
	g.perNode[nodeID] = n
	g.mu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { g.releaseOne(nodeID) }) }, n, true
}

// releaseOne hands a slot back, dropping the key at zero so the map stays
// bounded by the number of CONNECTED nodes rather than by every node the
// process has ever seen.
func (g *nodeStreamGate) releaseOne(nodeID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := g.perNode[nodeID] - 1
	if n <= 0 {
		delete(g.perNode, nodeID)
		return
	}
	g.perNode[nodeID] = n
}

// inFlight reports the current count for a node, for tests.
func (g *nodeStreamGate) inFlight(nodeID string) int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.perNode[nodeID]
}

// newInboundPacer builds this stream's own limiter, or nil when pacing is off.
//
// Per stream, so there is no map to sweep and no shared budget: one node cannot
// slow another down, and the limiter dies with the stream that owns it.
func (s *Server) newInboundPacer() *rate.Limiter {
	rps, burst := float64(defaultStreamMessagesPerSecond), defaultStreamBurst
	if s.config != nil {
		rps, burst = s.config.Quota.AgentStreamMessagesPerSecond, s.config.Quota.AgentStreamBurst
	}
	if rps <= 0 || burst <= 0 {
		return nil
	}
	return rate.NewLimiter(rate.Limit(rps), burst)
}

// pacedInbound reports whether a frame is subject to the inbound pace.
//
// Terminal frames are deliberately exempt: they carry PTY bytes to a browser,
// they touch no database, and pacing them would make an interactive shell feel
// broken. Everything else either writes to Postgres or fans out to every
// WebSocket client of the account, which is the work being bounded.
func pacedInbound(msg *proto.NodeToPlatform) bool {
	switch msg.GetPayload().(type) {
	case *proto.NodeToPlatform_TerminalOutput, *proto.NodeToPlatform_TerminalClosed:
		return false
	default:
		return true
	}
}
