package grpc

import (
	"strings"
	"sync"
	"testing"
)

// A peer-supplied identifier must be bounded before it keys a map or reaches a
// log line, and the truncation must be VISIBLE.
//
// Silent truncation would be its own bug: a short serial is a thing that
// legitimately happens (the agent sends the literal "unknown" on non-Pi
// hardware), so a quietly clipped value reads as a device reporting a short
// serial and sends the operator after the wrong problem.
func TestBoundedIdentifier(t *testing.T) {
	// The real shapes, which must pass through completely untouched. A Pi
	// serial is exactly 16, and the agent's own regex cannot produce anything
	// else.
	for _, real := range []string{
		"10000000abcd0001",                     // a Raspberry Pi serial
		"unknown",                              // non-Pi hardware
		"00000000-0000-0000-0000-000000000001", // a node/account UUID
		"",
	} {
		if got := boundedIdentifier(real); got != real {
			t.Errorf("a legitimate identifier must pass through unchanged: %q became %q", real, got)
		}
	}

	long := strings.Repeat("a", maxIdentifierBytes*4)
	got := boundedIdentifier(long)
	if len(got) >= len(long) {
		t.Fatalf("an over-long identifier must be truncated, got %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("truncation must be visible, got %q", got)
	}
	if !strings.HasPrefix(got, strings.Repeat("a", maxIdentifierBytes)) {
		t.Errorf("the first %d bytes must survive so the value stays identifying", maxIdentifierBytes)
	}

	// Exactly at the cap is not over it.
	exact := strings.Repeat("b", maxIdentifierBytes)
	if boundedIdentifier(exact) != exact {
		t.Error("a value exactly at the cap must not be truncated")
	}
}

// The gate bounds how MANY streams may be waiting to authenticate. The
// pre-auth deadline already bounds how LONG one lives, and the two are not the
// same thing: a peer can open streams far faster than 15 seconds retires them.
//
// The cap is whatever the gate was built with: cap acquires succeed, cap+1 is
// refused, and a released slot is reusable.
func TestPreAuthGateCapsConcurrencyAtTheConfiguredCap(t *testing.T) {
	const cap = 3
	g := newPreAuthGate(cap)

	if g.limit() != cap {
		t.Fatalf("limit() = %d, want the configured %d", g.limit(), cap)
	}
	for i := 0; i < cap; i++ {
		if !g.acquire() {
			t.Fatalf("acquire %d/%d was refused; the cap must clear a full fleet reconnect", i+1, cap)
		}
	}
	if g.acquire() {
		t.Fatal("the gate must refuse once the cap is reached, or it bounds nothing")
	}
	if g.inFlight() != cap {
		t.Fatalf("in flight = %d, want %d", g.inFlight(), cap)
	}

	// A slot handed back is reusable — this is what makes the cap a cap on
	// unauthenticated peers rather than on the fleet, since every agent passes
	// through this state and then releases on authentication.
	g.release()
	if !g.acquire() {
		t.Fatal("a released slot must be reusable")
	}
}

// An unset or non-positive cap selects the code default, never "unlimited".
// This is the one quota where 0 must not disable the bound: an unbounded
// pre-auth gate is exactly the hole it exists to close, and a hand-built Server
// in a test (newPreAuthGate(0)) must get a bounded one too.
//
// The default itself is pinned: it is the global half of the arithmetic in
// preauth_limits.go (256 connections per client IP at the edge x 4 streams per
// connection), and a quiet change to either side re-opens the single-address
// exhaustion the numbers were chosen against.
func TestPreAuthGateDefaultsWhenUnset(t *testing.T) {
	if defaultMaxPreAuthStreams != 256*maxConcurrentStreamsPerConnection {
		t.Fatalf("defaultMaxPreAuthStreams = %d, want 256 x %d = %d: the global cap must equal what one edge-bounded address can hold",
			defaultMaxPreAuthStreams, maxConcurrentStreamsPerConnection, 256*maxConcurrentStreamsPerConnection)
	}
	for _, unset := range []int{0, -1} {
		g := newPreAuthGate(unset)
		if g.limit() != defaultMaxPreAuthStreams {
			t.Fatalf("newPreAuthGate(%d).limit() = %d, want the default %d", unset, g.limit(), defaultMaxPreAuthStreams)
		}
		for i := 0; i < defaultMaxPreAuthStreams; i++ {
			if !g.acquire() {
				t.Fatalf("newPreAuthGate(%d): acquire %d/%d refused", unset, i+1, defaultMaxPreAuthStreams)
			}
		}
		if g.acquire() {
			t.Fatalf("newPreAuthGate(%d) must still be bounded at the default, not unlimited", unset)
		}
	}
}

// release must never drive the counter negative, or a double release would
// silently raise the effective cap.
func TestPreAuthGateReleaseIsFloored(t *testing.T) {
	const cap = 5
	g := newPreAuthGate(cap)
	g.release()
	g.release()
	if n := g.inFlight(); n != 0 {
		t.Fatalf("in flight = %d after releasing an empty gate, want 0", n)
	}
	for i := 0; i < cap; i++ {
		if !g.acquire() {
			t.Fatalf("the cap was reduced by a spurious release: refused at %d", i+1)
		}
	}
	if g.acquire() {
		t.Fatal("the cap must still hold")
	}
}

// The gate is touched by every incoming stream, so it is exercised
// concurrently. Run with -race.
func TestPreAuthGateIsConcurrencySafe(t *testing.T) {
	g := newPreAuthGate(0)
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.acquire() {
				g.release()
			}
		}()
	}
	wg.Wait()
	if n := g.inFlight(); n != 0 {
		t.Fatalf("every acquire was released, so in flight must be 0, got %d", n)
	}
}
