package grpc

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStampThrottleAllowsOncePerIntervalPerKey(t *testing.T) {
	th := newStampThrottle(time.Minute, 3)
	a, b := uuid.New(), uuid.New()
	now := time.Now()

	if !th.allow(a, now) {
		t.Fatal("first call for a key was refused")
	}
	if th.allow(a, now.Add(30*time.Second)) {
		t.Error("a second call inside the interval was allowed")
	}
	if !th.allow(b, now) {
		t.Error("another key was throttled by the first key's stamp")
	}
	if !th.allow(a, now.Add(61*time.Second)) {
		t.Error("a call after the interval was refused")
	}
}

// The key is peer-named, so the map is bounded; past the cap it resets
// rather than growing, at the price of one extra allowed stamp.
func TestStampThrottleIsBounded(t *testing.T) {
	th := newStampThrottle(time.Minute, 2)
	now := time.Now()
	first := uuid.New()
	th.allow(first, now)
	th.allow(uuid.New(), now)
	th.allow(uuid.New(), now) // third key: map was full, reset
	if len(th.last) > 2 {
		t.Errorf("throttle map grew to %d entries past a capacity of 2", len(th.last))
	}
	if !th.allow(first, now.Add(time.Second)) {
		t.Error("after the reset the first key should be allowed again (the SQL guard covers the gap)")
	}
}
