package terminal

import (
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
)

func capHandler(limit int) *Handler {
	cfg := &config.Config{}
	cfg.Terminal.MaxSessionsPerUser = limit
	return &Handler{cfg: cfg}
}

// The (N+1)th session is refused, not evicted: killing an operator's live
// root shell mid-command is worse than telling them to close one.
func TestSessionCapRefusesPastLimit(t *testing.T) {
	h := capHandler(2)
	user := uuid.New()

	r1 := h.acquireSessionSlot(user)
	r2 := h.acquireSessionSlot(user)
	if r1 == nil || r2 == nil {
		t.Fatal("sessions under the cap must be admitted")
	}
	if h.acquireSessionSlot(user) != nil {
		t.Fatal("the third session must be refused at cap 2")
	}
	// Another user is unaffected — the cap is per user, not global.
	other := uuid.New()
	if ro := h.acquireSessionSlot(other); ro == nil {
		t.Fatal("another user must not be blocked by the first user's cap")
	}
	// Releasing frees the slot.
	r1()
	if r := h.acquireSessionSlot(user); r == nil {
		t.Fatal("a released slot must be reusable")
	}
}

// Release is idempotent: the bridge teardown path can run a defer more than
// conceptually once (audit close + ws close), and a double decrement would
// quietly raise the effective cap.
func TestSessionCapReleaseIsIdempotent(t *testing.T) {
	h := capHandler(1)
	user := uuid.New()
	r := h.acquireSessionSlot(user)
	r()
	r()
	if got := h.acquireSessionSlot(user); got == nil {
		t.Fatal("slot must be available after release")
	}
	if h.acquireSessionSlot(user) != nil {
		t.Fatal("double release must not create a second slot")
	}
}

// 0 disables the cap entirely.
func TestSessionCapZeroIsUnlimited(t *testing.T) {
	h := capHandler(0)
	user := uuid.New()
	for i := 0; i < 50; i++ {
		if h.acquireSessionSlot(user) == nil {
			t.Fatalf("cap 0 must be unlimited (refused at %d)", i)
		}
	}
}
