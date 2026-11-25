package grpc

import (
	"strconv"
	"testing"
	"time"
)

// A rejected terminal frame writes a log line, and terminal frames are
// deliberately exempt from the inbound pacer — so an authenticated node could
// drive line-rate writes into a container log ring capped at 20 MB x 5 and flush
// the evidence of everything else, including the adoption-rejection and pre-auth
// records these limits exist to protect.
func TestRepeatsCollapseIntoOneLinePerWindow(t *testing.T) {
	th := newLogThrottle()
	now := time.Now()

	logged := 0
	for i := 0; i < 10_000; i++ {
		if ok, _ := th.allow("node-1", now); ok {
			logged++
		}
	}
	if logged != 1 {
		t.Fatalf("%d lines written for one node in one window, want 1", logged)
	}

	// The next window logs again, and says how many it swallowed.
	ok, suppressed := th.allow("node-1", now.Add(logThrottleWindow+time.Second))
	if !ok {
		t.Fatal("a new window did not log at all; the throttle must not become silence")
	}
	if suppressed != 9_999 {
		t.Fatalf("suppressed = %d, want 9999 — the count is what tells an operator a flood happened", suppressed)
	}
}

// Different nodes are throttled independently, or one noisy device hides every
// other one's frames.
func TestKeysAreThrottledIndependently(t *testing.T) {
	th := newLogThrottle()
	now := time.Now()

	if ok, _ := th.allow("node-1", now); !ok {
		t.Fatal("the first line for a key was suppressed")
	}
	if ok, _ := th.allow("node-2", now); !ok {
		t.Fatal("a second key was suppressed by the first key's window")
	}
}

// The map is peer-keyed, so it has to be bounded — and past the cap a key is not
// tracked AND not logged, because inserting anyway is how a bounded map stops
// being bounded.
func TestTheKeyMapStaysBounded(t *testing.T) {
	th := newLogThrottle()
	now := time.Now()

	for i := 0; i < logThrottleKeys*3; i++ {
		th.allow("node-"+strconv.Itoa(i), now)
	}
	if len(th.seen) > logThrottleKeys {
		t.Fatalf("the throttle map grew to %d keys, above the %d bound", len(th.seen), logThrottleKeys)
	}
}

// Expired keys are reclaimed, or the cap becomes permanent silence for whatever
// arrives after it.
func TestExpiredKeysAreReclaimed(t *testing.T) {
	th := newLogThrottle()
	now := time.Now()
	for i := 0; i < logThrottleKeys; i++ {
		th.allow("node-"+strconv.Itoa(i), now)
	}

	later := now.Add(2 * logThrottleWindow)
	if ok, _ := th.allow("fresh-node", later); !ok {
		t.Fatal("a new key was refused after the map filled, even though every entry had expired")
	}
}
