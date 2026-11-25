package grpc

import (
	"sync"
	"time"
)

// A rejected terminal frame writes a log line carrying a peer-supplied session
// id, and terminal frames are deliberately EXEMPT from the inbound pacer —
// they carry PTY bytes to a browser and touch no database, so pacing them would
// make an operator's shell stutter for no gain.
//
// The two together are a hole: one authenticated node can send rejected terminal
// frames at line rate, and each writes a line into a container log ring capped at
// 20 MB x 5. That flushes the evidence of whatever else was happening —
// including the adoption-rejection and pre-auth records these limits exist to
// protect.
//
// So the frames stay unpaced and the LOGGING is bounded instead: one line per
// key per window, carrying how many were suppressed, with the session id
// truncated by boundedIdentifier before it is ever formatted.

const (
	// logThrottleWindow is how long one key's repeats collapse into a single
	// line.
	logThrottleWindow = time.Minute
	// logThrottleKeys bounds the map, for the same reason every other
	// peer-keyed map here is bounded: a key an attacker chooses must not be a
	// way to grow memory. Past the cap a key is not tracked and its line is
	// suppressed — at that point the log is already saying "a flood", which is
	// the signal.
	logThrottleKeys = 4096
)

// logThrottle collapses repeated log lines per key.
type logThrottle struct {
	mu   sync.Mutex
	seen map[string]*throttleEntry
}

type throttleEntry struct {
	windowFrom time.Time
	suppressed int
}

func newLogThrottle() *logThrottle {
	return &logThrottle{seen: map[string]*throttleEntry{}}
}

// allow reports whether this occurrence should be logged, and how many were
// suppressed since the last line for this key.
func (t *logThrottle) allow(key string, now time.Time) (bool, int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.seen[key]
	if ok && now.Sub(entry.windowFrom) < logThrottleWindow {
		entry.suppressed++
		return false, 0
	}
	if !ok {
		if len(t.seen) >= logThrottleKeys {
			t.evictExpiredLocked(now)
		}
		// Still full: refuse to track, and refuse to log. Inserting anyway is
		// how a bounded map stops being bounded.
		if len(t.seen) >= logThrottleKeys {
			return false, 0
		}
		t.seen[key] = &throttleEntry{windowFrom: now}
		return true, 0
	}
	suppressed := entry.suppressed
	entry.windowFrom = now
	entry.suppressed = 0
	return true, suppressed
}

func (t *logThrottle) evictExpiredLocked(now time.Time) {
	for key, entry := range t.seen {
		if now.Sub(entry.windowFrom) >= logThrottleWindow {
			delete(t.seen, key)
		}
	}
}
