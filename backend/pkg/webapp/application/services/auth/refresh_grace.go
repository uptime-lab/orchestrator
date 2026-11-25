package auth

import (
	"sync"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
)

// Refresh-rotation grace for the client that just rotated.
//
// A refresh token is single-use, and presenting a spent one revokes its whole
// family (RefreshToken). That is right against a thief and wrong against two
// tabs of one browser that refresh at the same moment: the loser presents the
// token the winner has just spent, the family dies, and the user is signed
// out everywhere — including the winner's brand-new tokens.
//
// So for a few seconds after a rotation, a second presentation of the SAME
// token from the SAME client (User-Agent and address) is answered with the
// successor the first presentation minted, instead of being treated as reuse.
// Both tabs then hold one pair. Anything else is unchanged: a presentation
// from another client, or after the window, still revokes the family; and the
// successor is handed out only while its session is still live, so a logout
// or a password change inside the window ends it as before.
//
// A presentation that arrives while the first is still being rotated waits
// for it rather than racing it to the store. In memory, per process.
const (
	refreshReplayGrace = 10 * time.Second
	// refreshGraceEntries bounds the map. Each entry lives for the window
	// only; when full, a rotation simply gets no grace.
	refreshGraceEntries = 10000
)

type refreshGrace struct {
	mu      sync.Mutex
	entries map[string]*refreshGraceEntry
	now     func() time.Time
}

type refreshGraceEntry struct {
	// done is closed once the first presentation finished rotating.
	done   chan struct{}
	client refresh_token.Device
	// pair is the successor; nil when the rotation failed.
	pair *services.TokenPair
	// until ends the grace; zero while the rotation is in flight.
	until time.Time
}

func newRefreshGrace() *refreshGrace {
	return &refreshGrace{entries: map[string]*refreshGraceEntry{}, now: time.Now}
}

// begin registers a presentation of the token with this hash. leader is true
// for the presentation that must rotate; a follower receives the entry of the
// rotation already under way or just finished. A nil entry with leader true
// means the presentation is not tracked.
func (g *refreshGrace) begin(hash string, client refresh_token.Device) (entry *refreshGraceEntry, leader bool) {
	if g == nil || client == (refresh_token.Device{}) {
		// No client to compare against: no grace.
		return nil, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if e, ok := g.entries[hash]; ok {
		if e.until.IsZero() || now.Before(e.until) {
			return e, false
		}
		delete(g.entries, hash)
	}
	if len(g.entries) >= refreshGraceEntries {
		for k, e := range g.entries {
			if !e.until.IsZero() && !now.Before(e.until) {
				delete(g.entries, k)
			}
		}
		if len(g.entries) >= refreshGraceEntries {
			return nil, true
		}
	}
	e := &refreshGraceEntry{done: make(chan struct{}), client: client}
	g.entries[hash] = e
	return e, true
}

// finish records the leader's outcome and releases the followers. A failed
// rotation leaves nothing to share: the entry is dropped and every follower
// goes through the ordinary path.
func (g *refreshGrace) finish(hash string, e *refreshGraceEntry, pair *services.TokenPair, err error) {
	if g == nil || e == nil {
		return
	}
	g.mu.Lock()
	if err == nil && pair != nil {
		e.pair = pair
		e.until = g.now().Add(refreshReplayGrace)
	} else if g.entries[hash] == e {
		delete(g.entries, hash)
	}
	g.mu.Unlock()
	close(e.done)
}

// successorFor waits for the leader and returns its successor when this
// presentation qualifies for the grace: the same client, inside the window.
// nil means take the ordinary path.
func (g *refreshGrace) successorFor(e *refreshGraceEntry, client refresh_token.Device) *services.TokenPair {
	if g == nil || e == nil {
		return nil
	}
	select {
	case <-e.done:
	case <-time.After(refreshReplayGrace):
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if e.pair == nil || e.client != client || !g.now().Before(e.until) {
		return nil
	}
	return e.pair
}
