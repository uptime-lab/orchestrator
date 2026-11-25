package node

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// forceNetbootFakeRepo stubs only the two methods the decision touches; the
// embedded nil interface panics on anything else, which proves it reads and
// writes nothing more.
type forceNetbootFakeRepo struct {
	repository.NodeRepository
	stored  *node.Node
	updated *node.Node
}

func (f *forceNetbootFakeRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	return f.stored, nil
}

func (f *forceNetbootFakeRepo) Update(_ context.Context, n *node.Node) error {
	f.updated = n
	return nil
}

// UpdateNodeColumns mirrors the real repository: NAMED columns, not a whole row.
// The fake applies them to the stored node and hands that back as `updated`, so
// the assertions read the effect, and it rejects an unknown column because a
// typo in a column name is otherwise silent in the map form.
func (f *forceNetbootFakeRepo) UpdateNodeColumns(_ context.Context, _, _ uuid.UUID, columns map[string]interface{}) error {
	if f.stored == nil {
		return errors.New("not found")
	}
	for name, value := range columns {
		switch name {
		case "boot_to_os":
			f.stored.BootToOS, _ = value.(*bool)
		case "recovery_revert_pending":
			f.stored.RecoveryRevertPending, _ = value.(bool)
		case "recovery_prev_boot_to_os":
			f.stored.RecoveryPrevBootToOS, _ = value.(string)
		default:
			return fmt.Errorf("unexpected column %q", name)
		}
	}
	f.updated = f.stored
	return nil
}

// A failed install leaves the target deliberately unbootable — the agent never
// commits the image head unless the digest matched. Pointing the node at that
// disk drops it into an initramfs prompt nothing can reach; netboot hands it
// back to the platform.
func TestForceNetbootAfterFailedInstall(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()

	t.Run("boot target is forced off", func(t *testing.T) {
		on := true
		repo := &forceNetbootFakeRepo{stored: &node.Node{
			ID: uuid.New(), AccountID: accountID, BootToOS: &on,
		}}
		svc := guardService(repo)

		if err := svc.ForceNetbootAfterFailedInstall(ctx, accountID, repo.stored.ID); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.updated == nil {
			t.Fatal("node was not persisted")
		}
		if repo.updated.BootToOS == nil || *repo.updated.BootToOS {
			t.Errorf("boot_to_os must be forced off, got %v", repo.updated.BootToOS)
		}
	})

	// The reboot-to-recovery one-shot saves the previous boot target and
	// restores it on the first system-info report showing an http boot. If that
	// report lands AFTER a failed install, the restore would put boot_to_os
	// back to "on" and send the node at the disk we just invalidated. A failed
	// install voids the "put back what you had" contract: what they had was a
	// bootable disk.
	t.Run("pending recovery revert is cleared, not left to undo this", func(t *testing.T) {
		on := true
		repo := &forceNetbootFakeRepo{stored: &node.Node{
			ID:                    uuid.New(),
			AccountID:             accountID,
			BootToOS:              &on,
			RecoveryRevertPending: true,
			RecoveryPrevBootToOS:  "on",
		}}
		svc := guardService(repo)

		if err := svc.ForceNetbootAfterFailedInstall(ctx, accountID, repo.stored.ID); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.updated.RecoveryRevertPending {
			t.Error("a pending revert would restore boot_to_os and undo the failsafe")
		}
		if repo.updated.RecoveryPrevBootToOS != "" {
			t.Errorf("saved boot target must be dropped, got %q", repo.updated.RecoveryPrevBootToOS)
		}
		if repo.updated.BootToOS == nil || *repo.updated.BootToOS {
			t.Error("boot_to_os must still be off")
		}
	})
}
