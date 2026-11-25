package node_ssh_adopt

import (
	"sync"

	"github.com/google/uuid"
)

// Concurrency bounds for outbound SSH work. Each preflight or adoption holds a
// DNS lookup, a key decryption, an outbound handshake and a goroutine, on a
// server that also runs the control plane. Two bounds per kind: a server-wide
// one, and each account's SHARE of it, so one tenant on a shared install
// cannot hold every slot while another waits.
//
// The split is a judgement, not a measurement: adoption is a one-device-at-a-
// time flow on an on-prem install. Both server-wide values predate this file
// (adoptions) or match its shape (preflights); the per-account halves are half
// of each.
const (
	maxConcurrentAdoptions       = 4
	maxAdoptionsPerAccount       = 2
	maxConcurrentPreflights      = 8
	maxPreflightsPerAccount      = 4
	capacityKindAdoption    kind = "adoption"
	capacityKindPreflight   kind = "preflight"
)

type kind string

// capacityGate is a two-level semaphore: a server-wide slot count and a
// per-account one. acquire takes both or neither and returns the release;
// every exit path of the caller must run it, which is why it is a closure
// rather than two calls.
type capacityGate struct {
	kind       kind
	global     chan struct{}
	perAccount int

	mu   sync.Mutex
	held map[uuid.UUID]int
}

func newCapacityGate(k kind, global, perAccount int) *capacityGate {
	return &capacityGate{
		kind:       k,
		global:     make(chan struct{}, global),
		perAccount: perAccount,
		held:       map[uuid.UUID]int{},
	}
}

// acquire reserves one slot for the account. The per-account check runs
// first so a tenant at its own cap is told so rather than consuming a
// server-wide slot for the refusal.
func (g *capacityGate) acquire(accountID uuid.UUID) (release func(), err error) {
	g.mu.Lock()
	if g.held[accountID] >= g.perAccount {
		g.mu.Unlock()
		return nil, g.refusal(g.perAccount, true)
	}
	g.held[accountID]++
	g.mu.Unlock()

	select {
	case g.global <- struct{}{}:
	default:
		g.mu.Lock()
		g.decrement(accountID)
		g.mu.Unlock()
		return nil, g.refusal(cap(g.global), false)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			<-g.global
			g.mu.Lock()
			g.decrement(accountID)
			g.mu.Unlock()
		})
	}, nil
}

// decrement must run under g.mu. The map entry is removed at zero so the map
// stays bounded by the number of accounts with something in flight.
func (g *capacityGate) decrement(accountID uuid.UUID) {
	if g.held[accountID] <= 1 {
		delete(g.held, accountID)
		return
	}
	g.held[accountID]--
}

func (g *capacityGate) refusal(limit int, perAccount bool) error {
	switch g.kind {
	case capacityKindPreflight:
		return &PreflightCapacityError{Limit: limit, PerAccount: perAccount}
	default:
		return &AdoptionCapacityError{Limit: limit, PerAccount: perAccount}
	}
}

// inFlight reports how many slots the account holds (tests).
func (g *capacityGate) inFlight(accountID uuid.UUID) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held[accountID]
}

// globalInFlight reports how many server-wide slots are held (tests).
func (g *capacityGate) globalInFlight() int { return len(g.global) }
