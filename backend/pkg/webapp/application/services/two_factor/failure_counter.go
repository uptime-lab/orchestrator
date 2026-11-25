package two_factor

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// failureCounter bounds wrong-code attempts per user within a rolling window.
//
// Per USER, not per IP: an attacker who already holds the password can spread
// guesses across as many addresses as they like, so an IP-keyed limiter is no
// obstacle at all. In-memory is adequate — the window is minutes, and a
// backend restart costing an attacker their progress is not a problem worth
// a database round trip on every login.
type failureCounter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	entries map[uuid.UUID]failureEntry
}

type failureEntry struct {
	count     int
	expiresAt time.Time
}

func newFailureCounter(max int, window time.Duration) *failureCounter {
	return &failureCounter{max: max, window: window, entries: make(map[uuid.UUID]failureEntry)}
}

// exhausted reports whether the user has spent their attempts.
func (f *failureCounter) exhausted(userID uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.entries[userID]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(f.entries, userID)
		return false
	}
	return entry.count >= f.max
}

// record counts one wrong code. The window is refreshed on each failure, so a
// steady trickle of guesses cannot outlast it.
func (f *failureCounter) record(userID uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	entry, ok := f.entries[userID]
	if !ok || now.After(entry.expiresAt) {
		entry = failureEntry{}
	}
	entry.count++
	entry.expiresAt = now.Add(f.window)
	f.entries[userID] = entry
}

// reset clears the count after a correct code.
func (f *failureCounter) reset(userID uuid.UUID) {
	f.mu.Lock()
	delete(f.entries, userID)
	f.mu.Unlock()
}
