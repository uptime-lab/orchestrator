package grpc

import (
	"sync"
	"time"
)

// Bounds on what an UNAUTHENTICATED peer can make this process do.
//
// :50051 is typically open to the internet, and everything up to a validated
// token runs before the platform knows who is calling. Three bounds live in
// that window, and they share a shape: each must be bounded in the right
// dimension.
//
//   - The adoption-rejection tracker caps its map at 10 000 ENTRIES while the
//     key is a peer-supplied string of unbounded length. Ten thousand entries
//     is nothing; ten thousand entries whose keys are a megabyte each is
//     gigabytes, more than a small host has, reachable with a few hundred
//     messages — so the key length is bounded too.
//   - The same handshake logs peer-supplied node_id, account_id and
//     hardware_serial. The rejection log is throttled per source, but a fresh
//     source always logs its first line, so distinct giant strings are distinct
//     sources — the throttle does not apply. Container logs are capped at
//     20 MB x 5, so this evicts the evidence of whatever else was happening.
//   - How many pre-auth streams exist at once must be capped. The 15-second
//     pre-auth deadline (see EstablishStream) bounds how LONG one lives but not
//     how MANY, and an attacker can open them far faster than they expire. The
//     deadline only bounds the lifetime because the handler RETURNS on it:
//     cancelling a context is not enough, grpc-go's Recv does not observe one,
//     and a silent peer would park the handler and never give its slot back.
//
// The caps below are deliberately far above anything real, because the failure
// mode of a tight cap here is refusing a legitimate device.

const (
	// maxIdentifierBytes bounds a peer-supplied identifier before it is used as
	// a map key, stored, or written to a log line.
	//
	// Derived from the agent, not guessed: it extracts the serial with
	// `Serial\t*: ([a-z0-9]{16})` and sends the literal "unknown" when that
	// does not match, so an honest agent can only ever send 16 bytes or 7.
	// Node and account ids
	// are UUIDs at 36. So 128 is eight times the longest real value and leaves
	// room for a future non-Pi board reporting a DMI serial, while capping the
	// rejection map at roughly 10 000 x 128 bytes rather than 10 000 x anything.
	maxIdentifierBytes = 128

	// defaultMaxPreAuthStreams caps streams that have not yet authenticated when
	// AGENT_MAX_PREAUTH_STREAMS is unset (config.QuotaConfig.MaxPreAuthStreams).
	//
	// This gate is GLOBAL. It is one of two backend bounds, and the only one
	// that works without a client address. Without PROXY protocol the backend
	// never sees one: when a reverse proxy's TCP router forwards :50051 to
	// backend:50051, every agent arrives from the proxy's own address (see
	// noteAdoptionRejection). A per-IP dimension here would
	// have keyed the whole fleet on one address and BEEN the fleet-wide
	// refusal it was meant to stop, so without PROXY protocol the per-client
	// bound belongs at the edge, where the address is real: the operator's
	// proxy caps concurrent connections per client IP. That bound is sized to
	// a site's whole fleet (the bundled proxies take it from
	// PROXY_AGENT_CONNECTIONS_PER_ADDRESS), so it admits more connections x
	// maxConcurrentStreamsPerConnection (4) than this gate holds: without
	// PROXY protocol one address CAN fill it.
	//
	// The size: an honest reconnect storm (every node re-enters this state at
	// once when the backend restarts, and leaves it within a handshake) is
	// orders of magnitude below 1024 for any fleet this serves, and every
	// parked stream costs a goroutine, so a larger gate buys nothing against
	// a flood and costs memory.
	//
	// A bound per address below the whole gate needs the address: PROXY protocol v2
	// from the reverse proxy's TCP service plus the proxyproto listener in
	// Start (proxy_protocol.go), enabled on both sides together. With it,
	// AGENT_GRPC_PROXY_PROTOCOL=require arms preAuthSourceGate below, so one
	// address is now held to defaultMaxPreAuthStreamsPerSource of these 1024.
	defaultMaxPreAuthStreams = 1024

	// defaultMaxPreAuthStreamsPerSource caps pre-auth streams from ONE client
	// address when AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE is unset
	// (config.QuotaConfig.MaxPreAuthStreamsPerSource).
	//
	// 256 is the backend's own number; it does not mirror the edge's
	// per-address connection bound, a proxy setting sized to a site's whole
	// fleet. It counts only streams that have not authenticated yet, which an
	// honest device leaves within a handshake, so it bounds how many devices
	// behind one address may be mid-handshake at once, not how many may
	// connect; and it caps what one address can park regardless of how many
	// connections carry the streams. Filling the global gate costs four
	// distinct addresses.
	//
	// It is ARMED ONLY with PROXY protocol required (Server.preAuthSource is
	// nil otherwise). Keep that sentence: without a real address every peer is
	// the proxy, and a per-source key would BE the fleet-wide DoS.
	defaultMaxPreAuthStreamsPerSource = 256

	// maxConcurrentStreamsPerConnection bounds HTTP/2 streams on ONE connection.
	//
	// grpc-go's default is effectively unlimited. An honest agent holds exactly
	// ONE stream, transiently two while a reconnect overlaps a teardown
	// (postauth_limits.go); a reverse proxy in front of :50051 forwards TCP,
	// so each agent arrives on its own TCP connection rather than multiplexed
	// onto a shared one. 4 is twice the honest ceiling. It used to be 256 — equal to the
	// global pre-auth cap of the time, so ONE connection holding 256 silent
	// streams saturated the gate with no credentials and no second socket. Now
	// filling the gate from one address costs 256 separate connections — which
	// the edge's per-address bound, sized to a site's fleet, still admits — and,
	// with PROXY protocol required, the per-source gate refuses the 257th
	// stream from that address regardless of how many connections carry them.
	//
	// Stated honestly: this is NOT the fix for GHSA-hrxh-6v49-42gf. That is the
	// grpc-go bump to 1.82.1, and this bounds the same class of flood
	// independently of library version rather than replacing it.
	maxConcurrentStreamsPerConnection = 4
)

// preAuthDeadline is how long a stream may live without authenticating.
//
// A variable rather than a const so a test can drive the real reaper without
// waiting fifteen seconds for it. Nothing outside a test writes it.
var preAuthDeadline = 15 * time.Second

// boundedIdentifier makes a peer-supplied string safe to key a map on, or to
// put in a log line, by truncating it and saying so.
//
// The marker matters: a silently truncated value looks like a device reporting
// a short serial, which is a thing that legitimately happens ("unknown"), and
// an operator would debug the wrong problem.
func boundedIdentifier(v string) string {
	if len(v) <= maxIdentifierBytes {
		return v
	}
	return v[:maxIdentifierBytes] + "…(truncated)"
}

// preAuthGate counts streams that have not yet authenticated.
//
// A counter rather than a semaphore channel because a refused acquire must
// return immediately — blocking would hold the goroutine this exists to bound.
type preAuthGate struct {
	max int

	mu sync.Mutex
	n  int
}

// newPreAuthGate builds the gate with the configured cap. A non-positive max
// means "use the default", NOT "unlimited" — this is the one quota in the repo
// where 0 does not disable, because an unbounded pre-auth gate is precisely the
// hole it was built to close, and a hand-built Server in a test must not
// silently get one.
func newPreAuthGate(max int) *preAuthGate {
	if max <= 0 {
		max = defaultMaxPreAuthStreams
	}
	return &preAuthGate{max: max}
}

// limit reports the cap in force, for the log line that says why a stream was
// refused.
func (g *preAuthGate) limit() int {
	return g.max
}

// acquire takes a slot, or reports false when the cap is reached.
func (g *preAuthGate) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n >= g.max {
		return false
	}
	g.n++
	return true
}

// release returns a slot. Safe to call more than once via releaseOnce.
func (g *preAuthGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n > 0 {
		g.n--
	}
}

// inFlight reports the current count, for tests and for the log line that says
// why a stream was refused.
func (g *preAuthGate) inFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n
}

// preAuthSourceGate counts streams that have not yet authenticated PER CLIENT
// ADDRESS. It sits inside the global gate: a stream takes a global slot first,
// then a per-source one, and gives both back through the same release path.
//
// The map is keyed on peerSourceKey (proxy_protocol.go) — a bare, canonical IP
// — and an entry is deleted the moment its count reaches zero, so the map's
// size is bounded by the number of addresses with a stream IN FLIGHT right now,
// which the global gate already bounds. There is no separate cap on the map for
// that reason: it cannot outgrow the global gate.
//
// Armed only when the listener requires PROXY protocol; nil otherwise. Without
// a real client address every peer is the proxy, and counting per source would
// key the whole fleet on one address and BE the fleet-wide refusal.
type preAuthSourceGate struct {
	max int

	mu sync.Mutex
	n  map[string]int
}

// newPreAuthSourceGate builds the gate with the configured per-source cap. A
// non-positive max selects the default, never "unlimited" — the same rule as
// newPreAuthGate, for the same reason.
func newPreAuthSourceGate(max int) *preAuthSourceGate {
	if max <= 0 {
		max = defaultMaxPreAuthStreamsPerSource
	}
	return &preAuthSourceGate{max: max, n: map[string]int{}}
}

// limit reports the per-source cap in force, for the refusal log line.
func (g *preAuthSourceGate) limit() int {
	return g.max
}

// acquire takes a slot for source, or reports false when that source is at
// its cap. Other sources are unaffected — that is the whole point.
func (g *preAuthSourceGate) acquire(source string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n[source] >= g.max {
		return false
	}
	g.n[source]++
	return true
}

// release returns a slot for source and drops the entry at zero, so a source
// that has gone quiet costs nothing. Safe against a double release: an absent
// key stays absent.
func (g *preAuthSourceGate) release(source string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n, ok := g.n[source]
	if !ok {
		return
	}
	if n <= 1 {
		delete(g.n, source)
		return
	}
	g.n[source] = n - 1
}

// inFlight reports the current count for source; zero for an unknown one.
func (g *preAuthSourceGate) inFlight(source string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.n[source]
}

// sources reports how many addresses currently hold a slot, for the test that
// proves the map does not grow with addresses that have left.
func (g *preAuthSourceGate) sources() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.n)
}
