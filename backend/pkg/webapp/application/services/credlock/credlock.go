// Package credlock provides a per-user in-process lock that serializes
// last-credential removals across the services that can each strip a sign-in
// method (passkey.Delete, auth.UnlinkOIDC, and password changes).
//
// The invariant those guards enforce — "a user must always keep at least one
// way to sign in" — is a check-then-act: count the remaining methods, then
// remove one. Two concurrent removals for the same user each counted ≥1
// remaining and each proceeded, together removing the last two and locking the
// account out. A shared lock keyed on the user id makes the
// count-and-remove atomic against the other removal paths.
//
// It is in-process on purpose: this backend runs as a SINGLE instance per
// install (the gRPC stream registry, the workflow worker pool and the node
// monitor are all in-process singletons — horizontal scaling is not a supported
// deployment), so a process-local lock is the whole boundary. A second backend
// against the same database would need a Postgres advisory lock instead; that
// is a deliberate non-goal here, noted so the assumption is visible if it ever
// changes.
package credlock

import (
	"sync"

	"github.com/google/uuid"
)

// Locker hands out one mutex per user id, created on first use.
type Locker struct {
	mu    sync.Mutex
	locks map[uuid.UUID]*sync.Mutex
}

// New builds an empty locker.
func New() *Locker {
	return &Locker{locks: make(map[uuid.UUID]*sync.Mutex)}
}

// forUser returns the user's mutex, allocating it under the map lock. The map
// only grows; the entries are a handful of bytes each and the user set is
// bounded by the tenant, so a reaper would be more machinery than it saves.
func (l *Locker) forUser(userID uuid.UUID) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.locks[userID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[userID] = m
	}
	return m
}

// WithUser runs fn while holding the user's lock, so a concurrent
// credential-removal path for the same user waits rather than racing the count.
func (l *Locker) WithUser(userID uuid.UUID, fn func() error) error {
	m := l.forUser(userID)
	m.Lock()
	defer m.Unlock()
	return fn()
}
