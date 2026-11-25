package access

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
)

// Taking over an account that already exists on the device with no key and
// no password would clear its password, lock it and end its sessions on
// arrival — the stock account an operator is still logged in with. Refused
// before anything is saved or sent; a key or a password lets it through.
func TestAdoptingAnExistingAccountNeedsACredential(t *testing.T) {
	h := newAuditHarness(t)
	ctx := context.Background()

	_, err := h.svc.CreateUserEntry(ctx, CreateUserEntryRequest{
		AccountID: h.accountID, ActorUserID: h.actorID, NodeID: &h.nodeID,
		Username: "ubuntu", Sudo: true, AdoptUnmanaged: true,
	})
	if !errors.Is(err, access_entry.ErrAdoptNeedsCredential) {
		t.Fatalf("create err = %v, want ErrAdoptNeedsCredential", err)
	}
	if len(h.entries.byID) != 0 {
		t.Fatal("the entry was saved despite the refusal")
	}

	if _, err := h.svc.CreateUserEntry(ctx, CreateUserEntryRequest{
		AccountID: h.accountID, ActorUserID: h.actorID, NodeID: &h.nodeID,
		Username: "ubuntu", Sudo: true, AdoptUnmanaged: true, KeyIDs: []uuid.UUID{h.keyID},
	}); err != nil {
		t.Fatalf("adopt with a key: %v", err)
	}

	// Turning adoption on for an entry that has neither is refused the same way.
	plain, err := h.svc.CreateUserEntry(ctx, CreateUserEntryRequest{
		AccountID: h.accountID, ActorUserID: h.actorID, NodeID: &h.nodeID, Username: "pi",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	on := true
	if _, err := h.svc.UpdateEntry(ctx, UpdateEntryRequest{
		AccountID: h.accountID, ActorUserID: h.actorID, EntryID: plain.ID, AdoptUnmanaged: &on,
	}); !errors.Is(err, access_entry.ErrAdoptNeedsCredential) {
		t.Fatalf("update err = %v, want ErrAdoptNeedsCredential", err)
	}
}
