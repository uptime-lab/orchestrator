package grpc

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// deletedRefusalStampInterval is how often, per deleted node id, the refusal
// branch may ask the service to stamp the tombstone. The service applies the
// same interval in SQL; this copy exists so the common case — a stranded agent
// redialling every second — never reaches the database at all.
const deletedRefusalStampInterval = time.Minute

// deletedRefusalStampCapacity bounds the throttle map. The key is a node id
// the PEER named, so an unbounded map is peer-controlled memory; past the cap
// the map is cleared, which only makes the throttle more permissive for one
// interval — the SQL condition still holds the line.
const deletedRefusalStampCapacity = 10_000

// stampThrottle answers "may this key act again now" at most once per
// interval, with a bounded memory.
type stampThrottle struct {
	mu       sync.Mutex
	interval time.Duration
	capacity int
	last     map[uuid.UUID]time.Time
}

func newStampThrottle(interval time.Duration, capacity int) *stampThrottle {
	return &stampThrottle{interval: interval, capacity: capacity, last: map[uuid.UUID]time.Time{}}
}

// allow reports whether the key's last allowed action is older than the
// interval, and records now as the last action when it is.
func (t *stampThrottle) allow(key uuid.UUID, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.interval {
		return false
	}
	if len(t.last) >= t.capacity {
		t.last = map[uuid.UUID]time.Time{}
	}
	t.last[key] = now
	return true
}
