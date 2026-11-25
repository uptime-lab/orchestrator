package auth

import (
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// Per-address brake on password guessing.
//
// The /auth routes carry a per-IP limiter, which says nothing about guesses
// against ONE account spread over many addresses. This bounds those, in two
// layers:
//
//   - Per address AND source (the server's trusted client IP): after a few
//     failures, each further failure closes that address to password checks
//     FROM THAT SOURCE for a delay that doubles up to a short cap. A stranger
//     who keeps failing for someone else's address closes it only for
//     themselves; the owner, from a source that has not failed, is not held.
//   - Per address across every source: after many more failures, sources
//     that have ALREADY failed for this address share one attempt per
//     spacing interval. A source that has not failed for the address is
//     never refused by this layer, so a correct password from it always
//     reaches the comparison; past the ceiling a guesser needs a fresh
//     source per guess beyond that rate.
//
// An attempt is counted when it is ADMITTED, before the password is compared,
// and a success refunds it; so parallel attempts cannot all slip past the
// check while the first comparison is still running. Refused attempts are
// answered before the password is compared and do not extend any delay.
// Passkey and federated sign-ins are not braked at all.
//
// Keyed by the normalized address as typed, not by the user: an address
// nobody holds is braked exactly like one somebody does, so the brake answers
// nothing about which accounts exist. In memory, like the burst detector next
// to it, and per process.
const (
	// loginThrottleFreeFailures is how many failures an address gets from one
	// source before any delay: room for a person to mistype twice and reach
	// for a password manager.
	loginThrottleFreeFailures = 5
	// loginThrottleBaseDelay is the first delay; each further failure doubles
	// it.
	loginThrottleBaseDelay = 2 * time.Second
	// loginThrottleMaxDelay caps the per-source delay: a sustained guesser
	// gets about one attempt per minute per address from each source.
	loginThrottleMaxDelay = time.Minute
	// loginThrottleAddressFreeFailures is how many failures an address takes
	// across all sources before the address-wide spacing applies.
	loginThrottleAddressFreeFailures = 20
	// loginThrottleAddressSpacing is the address-wide interval: past the
	// ceiling, sources that have already failed for the address share one
	// attempt per interval.
	loginThrottleAddressSpacing = 10 * time.Second
	// loginThrottleReset forgets failures after this long without a new one.
	loginThrottleReset = 15 * time.Minute
	// loginThrottleKeys bounds each map; see evictLocked.
	loginThrottleKeys = 10000
)

type loginThrottle struct {
	mu sync.Mutex
	// pairs is keyed by address and source, addresses by address alone.
	pairs     map[string]*loginThrottleEntry
	addresses map[string]*loginThrottleEntry
	now       func() time.Time
}

type loginThrottleEntry struct {
	failures     int
	lastFailure  time.Time
	blockedUntil time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{
		pairs:     map[string]*loginThrottleEntry{},
		addresses: map[string]*loginThrottleEntry{},
		now:       time.Now,
	}
}

func throttleKey(email string) string { return user.NormalizeEmail(email) }

func throttlePairKey(key, source string) string { return key + "\x00" + source }

// admit decides whether a password check for the address from this source
// may run. A positive duration refuses it for that long; zero admits it AND
// counts it as a failure until succeed refunds it.
func (t *loginThrottle) admit(email, source string) time.Duration {
	if t == nil {
		return 0
	}
	key := throttleKey(email)
	if key == "" {
		return 0
	}
	pairKey := throttlePairKey(key, source)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()

	pair := freshEntry(t.pairs[pairKey], now)
	if pair != nil {
		if d := pair.blockedUntil.Sub(now); d > 0 {
			return d
		}
	}
	addr := freshEntry(t.addresses[key], now)
	failedHere := pair != nil && pair.failures > 0
	if failedHere && addr != nil && addr.failures >= loginThrottleAddressFreeFailures {
		if d := addr.blockedUntil.Sub(now); d > 0 {
			return d
		}
	}

	pair = t.entryLocked(t.pairs, pairKey, now)
	pair.failures++
	pair.lastFailure = now
	if over := pair.failures - loginThrottleFreeFailures; over > 0 {
		delay := loginThrottleMaxDelay
		if over <= 6 { // 2s << 5 = 64s, already past the cap
			if d := loginThrottleBaseDelay << (over - 1); d < delay {
				delay = d
			}
		}
		pair.blockedUntil = now.Add(delay)
	}

	addr = t.entryLocked(t.addresses, key, now)
	addr.failures++
	addr.lastFailure = now
	if addr.failures > loginThrottleAddressFreeFailures {
		addr.blockedUntil = now.Add(loginThrottleAddressSpacing)
	}
	return 0
}

// succeed refunds an admitted attempt whose password was right: the owner got
// in, so the address's failures are forgotten.
func (t *loginThrottle) succeed(email, source string) {
	if t == nil {
		return
	}
	key := throttleKey(email)
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pairs, throttlePairKey(key, source))
	delete(t.addresses, key)
}

// freshEntry returns e, reset if its failures are older than the reset
// window; nil stays nil.
func freshEntry(e *loginThrottleEntry, now time.Time) *loginThrottleEntry {
	if e != nil && now.Sub(e.lastFailure) > loginThrottleReset {
		*e = loginThrottleEntry{}
	}
	return e
}

func (t *loginThrottle) entryLocked(m map[string]*loginThrottleEntry, key string, now time.Time) *loginThrottleEntry {
	if e, ok := m[key]; ok {
		return freshEntry(e, now)
	}
	if len(m) >= loginThrottleKeys {
		evictLocked(m, now)
	}
	e := &loginThrottleEntry{}
	m[key] = e
	return e
}

// evictLocked makes room in a full map. Stale entries go first. If every
// entry is live — itself an attack across many addresses — one entry is
// evicted rather than the new one dropped, which would leave the new address
// unbraked: an entry not currently blocked with the fewest failures (oldest
// first), else the blocked entry whose block ends soonest. A braked target
// therefore outlives a flood of single failures.
func evictLocked(m map[string]*loginThrottleEntry, now time.Time) {
	for key, e := range m {
		if now.Sub(e.lastFailure) > loginThrottleReset {
			delete(m, key)
		}
	}
	if len(m) < loginThrottleKeys {
		return
	}
	var victim string
	var best *loginThrottleEntry
	for key, e := range m {
		if best == nil || lessHarmful(e, best, now) {
			victim, best = key, e
		}
	}
	delete(m, victim)
}

// lessHarmful reports whether forgetting a costs the brake less than
// forgetting b.
func lessHarmful(a, b *loginThrottleEntry, now time.Time) bool {
	aBlocked, bBlocked := a.blockedUntil.After(now), b.blockedUntil.After(now)
	if aBlocked != bBlocked {
		return !aBlocked
	}
	if aBlocked {
		return a.blockedUntil.Before(b.blockedUntil)
	}
	if a.failures != b.failures {
		return a.failures < b.failures
	}
	return a.lastFailure.Before(b.lastFailure)
}
