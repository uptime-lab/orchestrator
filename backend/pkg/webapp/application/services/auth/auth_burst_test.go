package auth

import (
	"testing"
	"time"
)

func TestAuthBurstRaisesOnceThenGoesQuiet(t *testing.T) {
	tracker := newAuthBurstTracker()
	now := time.Now()

	for i := 1; i < authBurstThreshold; i++ {
		if burst, _ := tracker.note("203.0.113.7", now); burst {
			t.Fatalf("raised at attempt %d, below the threshold of %d", i, authBurstThreshold)
		}
	}
	burst, count := tracker.note("203.0.113.7", now)
	if !burst {
		t.Fatalf("did not raise at attempt %d", authBurstThreshold)
	}
	if count != authBurstThreshold {
		t.Fatalf("reported %d failures, want %d", count, authBurstThreshold)
	}

	// A sustained attack must not write an anomaly per attempt: the row already
	// counts occurrences, and the writes would amplify the load being attacked.
	for i := 0; i < 100; i++ {
		if again, _ := tracker.note("203.0.113.7", now.Add(time.Minute)); again {
			t.Fatal("raised again inside the same window")
		}
	}
}

func TestAuthBurstCountsPerSource(t *testing.T) {
	tracker := newAuthBurstTracker()
	now := time.Now()
	for i := 0; i < authBurstThreshold; i++ {
		tracker.note("198.51.100.1", now)
		if burst, _ := tracker.note("198.51.100.2", now); burst && i < authBurstThreshold-1 {
			t.Fatal("one source's failures counted towards another's")
		}
	}
}

// A person mistyping their password, walking away, and coming back tomorrow
// must never accumulate into an alert.
func TestAuthBurstWindowExpires(t *testing.T) {
	tracker := newAuthBurstTracker()
	now := time.Now()
	for i := 0; i < authBurstThreshold-1; i++ {
		tracker.note("192.0.2.5", now)
	}
	if burst, count := tracker.note("192.0.2.5", now.Add(authBurstWindow+time.Minute)); burst || count != 1 {
		t.Fatalf("after the window the count was %d (burst=%v), want a fresh 1", count, burst)
	}
}

// The tracking map is fed by whoever is attacking, so it must be bounded.
func TestAuthBurstBoundsTrackedSources(t *testing.T) {
	tracker := newAuthBurstTracker()
	now := time.Now()
	for i := 0; i < authBurstSources+500; i++ {
		tracker.note(string(rune(i%65536))+"-source", now)
	}
	if len(tracker.sources) > authBurstSources {
		t.Fatalf("tracking %d sources, above the %d bound", len(tracker.sources), authBurstSources)
	}
}

// An empty source (a request with no usable address) must not become a bucket
// every unattributable failure piles into.
func TestAuthBurstIgnoresAnEmptySource(t *testing.T) {
	tracker := newAuthBurstTracker()
	now := time.Now()
	for i := 0; i < authBurstThreshold*2; i++ {
		if burst, _ := tracker.note("", now); burst {
			t.Fatal("raised for an unattributable failure")
		}
	}
	if len(tracker.sources) != 0 {
		t.Fatal("an empty source was tracked")
	}
}
