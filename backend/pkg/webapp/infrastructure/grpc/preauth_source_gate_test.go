package grpc

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc/peer"
)

// The per-source gate counts one address independently of every other, gives
// slots back exactly, and forgets an address the moment its count reaches zero
// — the last being what keeps the map bounded by streams in flight rather than
// by addresses ever seen.
func TestPreAuthSourceGateBoundsEachSourceIndependently(t *testing.T) {
	g := newPreAuthSourceGate(2)
	if g.limit() != 2 {
		t.Fatalf("limit = %d, want 2", g.limit())
	}

	if !g.acquire("a") || !g.acquire("a") {
		t.Fatal("source a must get its two slots")
	}
	if g.acquire("a") {
		t.Fatal("source a got a third slot past a cap of 2")
	}
	// Another source is untouched by a's saturation — the property the global
	// gate could never offer.
	if !g.acquire("b") {
		t.Fatal("source b was refused while only a is at its cap")
	}
	if got := g.inFlight("a"); got != 2 {
		t.Fatalf("inFlight(a) = %d, want 2", got)
	}
	if got := g.inFlight("b"); got != 1 {
		t.Fatalf("inFlight(b) = %d, want 1", got)
	}
	if got := g.sources(); got != 2 {
		t.Fatalf("sources = %d, want 2", got)
	}

	g.release("a")
	if !g.acquire("a") {
		t.Fatal("a released slot must be reusable by the same source")
	}
	g.release("a")
	g.release("a")
	g.release("b")
	if got := g.sources(); got != 0 {
		t.Fatalf("sources = %d after every slot was returned; an address at zero must be deleted so the map cannot grow", got)
	}
	if got := g.inFlight("a"); got != 0 {
		t.Fatalf("inFlight(a) = %d after full release, want 0", got)
	}

	// A double release is inert: nothing goes negative and no entry appears.
	g.release("a")
	g.release("never-acquired")
	if got := g.sources(); got != 0 {
		t.Fatalf("a release of an unknown source created an entry: sources = %d", got)
	}
	if !g.acquire("a") || !g.acquire("a") || g.acquire("a") {
		t.Fatal("after a spurious release the cap must still be exactly 2")
	}
}

// Zero and negative select the code default, never unlimited — the same rule as
// the global gate, and for the same reason.
func TestPreAuthSourceGateNonPositiveMaxIsTheDefaultNotUnlimited(t *testing.T) {
	for _, n := range []int{0, -1} {
		if got := newPreAuthSourceGate(n).limit(); got != defaultMaxPreAuthStreamsPerSource {
			t.Fatalf("newPreAuthSourceGate(%d).limit() = %d, want the default %d", n, got, defaultMaxPreAuthStreamsPerSource)
		}
	}
	if defaultMaxPreAuthStreamsPerSource*4 != defaultMaxPreAuthStreams {
		t.Fatalf("per-source default %d x 4 addresses must equal the global default %d — the design is that filling the gate costs four addresses",
			defaultMaxPreAuthStreamsPerSource, defaultMaxPreAuthStreams)
	}
}

// rawAddr is a net.Addr whose String() is exactly what the test says. It exists
// because *net.TCPAddr already prints a 16-byte v4-mapped IP in dotted form, so
// a TCPAddr can never present "[::ffff:a.b.c.d]:port" to the key function —
// only a transport with its own address type can, and that is the spelling the
// unmap exists for. (Verified: with the Unmap call removed, every TCPAddr case
// below still passed; this type is what makes the fold observable.)
type rawAddr string

func (rawAddr) Network() string  { return "tcp" }
func (a rawAddr) String() string { return string(a) }

// One address, one bucket: the port is dropped and an IPv4 address spelled as
// v4-in-v6 folds onto its IPv4 form, or a peer that alternates spellings gets
// two caps.
func TestSourceKeyCanonicalisesTheAddress(t *testing.T) {
	cases := []struct {
		name string
		addr net.Addr
		want string
	}{
		{"v4 host:port", &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 40123}, "203.0.113.7"},
		{"same v4, other port", &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 40124}, "203.0.113.7"},
		{"v4 mapped into v6, TCPAddr", &net.TCPAddr{IP: net.ParseIP("::ffff:203.0.113.7"), Port: 40125}, "203.0.113.7"},
		{"v4 mapped into v6, literal", rawAddr("[::ffff:203.0.113.7]:40125"), "203.0.113.7"},
		{"v4 mapped into v6, literal, no port", rawAddr("::ffff:203.0.113.7"), "203.0.113.7"},
		{"plain v6", &net.TCPAddr{IP: net.ParseIP("2001:db8::7"), Port: 40126}, "2001:db8::7"},
		{"v6 with zone", &net.TCPAddr{IP: net.ParseIP("fe80::1"), Port: 1, Zone: "eth0"}, "fe80::1"},
		{"v6 with zone, literal", rawAddr("[fe80::1%eth0]:1"), "fe80::1"},
		{"unix socket keeps its own identity", &net.UnixAddr{Name: "/run/agent.sock", Net: "unix"}, "/run/agent.sock"},
	}
	for _, tc := range cases {
		if got := sourceKeyFromAddr(tc.addr); got != tc.want {
			t.Errorf("%s: sourceKeyFromAddr(%v) = %q, want %q", tc.name, tc.addr, got, tc.want)
		}
	}
	// The property stated as a collision: two spellings of one address must
	// land in ONE bucket.
	a := sourceKeyFromAddr(rawAddr("203.0.113.7:1"))
	b := sourceKeyFromAddr(rawAddr("[::ffff:203.0.113.7]:2"))
	if a != b {
		t.Fatalf("two spellings of one address key two buckets: %q vs %q", a, b)
	}
}

// A stream with no peer at all is not keyed — the caller falls back to the
// global gate rather than counting every such stream against one shared bucket.
func TestPeerSourceKeyReportsNoPeer(t *testing.T) {
	if key, ok := peerSourceKey(context.Background()); ok {
		t.Fatalf("a context with no peer produced a key %q", key)
	}
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("::ffff:198.51.100.9"), Port: 5}})
	key, ok := peerSourceKey(ctx)
	if !ok || key != "198.51.100.9" {
		t.Fatalf("peerSourceKey = (%q, %v), want (198.51.100.9, true)", key, ok)
	}
	// A peer struct with a nil address is the same as no peer.
	if key, ok := peerSourceKey(peer.NewContext(context.Background(), &peer.Peer{})); ok {
		t.Fatalf("a nil peer address produced a key %q", key)
	}
}
