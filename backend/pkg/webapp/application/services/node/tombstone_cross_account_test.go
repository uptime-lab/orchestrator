package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// scopedTombstoneRepo answers the way the SQL repository's WHERE clauses do:
// every account-taking method matches on (id, account_id), so a record is
// invisible to any other account.
type scopedTombstoneRepo struct {
	repository.NodeTombstoneRepository
	rows map[uuid.UUID]*node_tombstone.Tombstone
}

func (r *scopedTombstoneRepo) find(accountID, id uuid.UUID) (*node_tombstone.Tombstone, error) {
	t, ok := r.rows[id]
	if !ok || t.AccountID != accountID {
		return nil, app_errors.ErrNotFound
	}
	return t, nil
}

func (r *scopedTombstoneRepo) Create(_ context.Context, t *node_tombstone.Tombstone) error {
	copied := *t
	r.rows[t.ID] = &copied
	return nil
}

func (r *scopedTombstoneRepo) ListByAccountID(_ context.Context, accountID uuid.UUID) ([]node_tombstone.Tombstone, error) {
	var out []node_tombstone.Tombstone
	for _, t := range r.rows {
		if t.AccountID == accountID {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (r *scopedTombstoneRepo) GetByID(_ context.Context, accountID, id uuid.UUID) (*node_tombstone.Tombstone, error) {
	t, err := r.find(accountID, id)
	if err != nil {
		return nil, err
	}
	copied := *t
	return &copied, nil
}

func (r *scopedTombstoneRepo) MarkRestored(_ context.Context, accountID, id uuid.UUID) error {
	t, err := r.find(accountID, id)
	if err != nil {
		return err
	}
	now := time.Now()
	t.RestoredAt = &now
	return nil
}

func (r *scopedTombstoneRepo) MarkIdentityReleased(_ context.Context, accountID, id uuid.UUID, at time.Time) error {
	t, err := r.find(accountID, id)
	if err != nil || t.IdentityReleasedAt != nil {
		return app_errors.ErrNotFound
	}
	t.IdentityReleasedAt = &at
	return nil
}

func (r *scopedTombstoneRepo) MarkRefused(_ context.Context, accountID, id uuid.UUID, at time.Time, _ time.Duration) error {
	if t, err := r.find(accountID, id); err == nil {
		t.LastRefusedAt = &at
	}
	return nil
}

func (r *scopedTombstoneRepo) Delete(_ context.Context, accountID, id uuid.UUID) error {
	if _, err := r.find(accountID, id); err != nil {
		return err
	}
	delete(r.rows, id)
	return nil
}

// A node deleted in account A is never restored by, or into, account B. Every
// service entry point that reads or writes a deleted-node record is driven as
// B — a super admin of B holds node.delete there, so authorization passes and
// only the account scope stands between B and A's record — and each answers
// not-found or does nothing, while A's record stays exactly as it was and A's
// own restore still works.
func TestADeletedNodeIsNeverReachableFromAnotherAccount(t *testing.T) {
	svc, nodes, accountA, nodeID := newScopedCleanupService(t)
	const serial = "10000000abcd0001"
	nodes.nodes[nodeID].Fingerprint = &node.HardwareFingerprint{HardwareSerial: serial}
	tombstones := &scopedTombstoneRepo{rows: map[uuid.UUID]*node_tombstone.Tombstone{}}
	svc.tombstoneRepo = tombstones
	if err := svc.Delete(context.Background(), accountA, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete in A: %v", err)
	}
	before := *tombstones.rows[nodeID]
	accountB, adminB := uuid.New(), uuid.New()
	ctx := context.Background()

	if got, err := svc.ListDeleted(ctx, accountB); err != nil || len(got) != 0 {
		t.Errorf("B's deleted-node list = %d records, err %v; want none of A's", len(got), err)
	}
	if n, err := svc.RestoreDeleted(ctx, accountB, adminB, nodeID); !errors.Is(err, app_errors.ErrNotFound) {
		t.Errorf("B's restore of A's record answered %v, %v; want not found", n, err)
	}
	if _, ok := nodes.nodes[nodeID]; ok {
		t.Fatal("B's restore attempt wrote a node row")
	}
	if err := svc.ForgetDeleted(ctx, accountB, adminB, nodeID); !errors.Is(err, app_errors.ErrNotFound) {
		t.Errorf("B's forget of A's record answered %v; want not found", err)
	}
	svc.NoteDeletedNodeRefused(ctx, accountB, nodeID, before.UniqueToken, serial)
	if err := svc.RecordIdentityReleased(ctx, accountB, nodeID); err != nil {
		t.Errorf("identity-release report under B: %v", err)
	}

	after, ok := tombstones.rows[nodeID]
	if !ok {
		t.Fatal("A's record is gone after B's attempts")
	}
	if after.AccountID != accountA || after.RestoredAt != nil || after.LastRefusedAt != nil || after.IdentityReleasedAt != nil {
		t.Errorf("A's record changed under B's attempts: before %+v, after %+v", before, *after)
	}

	restored, err := svc.RestoreDeleted(ctx, accountA, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("A's own restore after B's attempts: %v", err)
	}
	if restored.AccountID != accountA {
		t.Errorf("restored into account %s, want A %s", restored.AccountID, accountA)
	}
}
