package auth

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/refresh_token"
)

// ticketStore makes a short-lived, otherwise stateless ticket single-use.
//
// The two-factor ticket is a signed JWT, which means it stays valid for its
// whole TTL no matter how many times it is presented. That turns one correct
// password into a window of unlimited code guesses. Recording issued tickets
// and consuming them on use closes that, at the cost of a small in-memory map
// whose entries live for minutes.
//
// Only the DIGEST is held, so a heap dump does not yield usable tickets. The
// user id is held beside it so a global revocation can end the user's pending
// challenges too (revokeUser): "sign out everywhere" must include a login that
// is halfway through, or the button leaves exactly one door open.
type ticketStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]pendingTicket
}

type pendingTicket struct {
	userID uuid.UUID
	expiry time.Time
}

func newTicketStore(ttl time.Duration) *ticketStore {
	return &ticketStore{ttl: ttl, entries: make(map[string]pendingTicket)}
}

func (t *ticketStore) issue(ticket string, userID uuid.UUID) {
	key := refresh_token.Hash(ticket)
	now := time.Now()

	t.mu.Lock()
	defer t.mu.Unlock()
	for k, entry := range t.entries {
		if now.After(entry.expiry) {
			delete(t.entries, k)
		}
	}
	t.entries[key] = pendingTicket{userID: userID, expiry: now.Add(t.ttl)}
}

// revokeUser ends every pending challenge the user holds. Called from the
// session revocations; the password-change case needs no call, because the
// consumer compares the ticket's issue time with PasswordChangedAt.
func (t *ticketStore) revokeUser(userID uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, entry := range t.entries {
		if entry.userID == userID {
			delete(t.entries, k)
		}
	}
}

// consume reports whether the ticket was outstanding, and spends it.
//
// An unknown ticket is REJECTED rather than waved through: a backend restart
// loses the map, which costs a user in mid-login one retry, whereas accepting
// unknown tickets would restore exactly the replayability this exists to stop.
func (t *ticketStore) consume(ticket string) bool {
	key := refresh_token.Hash(ticket)

	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.entries[key]
	delete(t.entries, key)
	return ok && time.Now().Before(entry.expiry)
}
