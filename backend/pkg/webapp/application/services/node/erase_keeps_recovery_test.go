package node

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// After an erase run from the recovery system the node stays on recovery,
// whatever target it had, and the reboot-to-recovery one-shot is voided so
// its restore cannot put "on" back. A node on its own OS keeps its target:
// the device refuses to erase the disk it runs from.
func TestForceNetbootAfterDiskErase(t *testing.T) {
	ctx := context.Background()
	account := uuid.New()

	t.Run("in recovery: kept on recovery", func(t *testing.T) {
		n := bootReportingNode(account, "http", true)
		on := true
		n.BootToOS = &on
		n.RecoveryRevertPending = true
		n.RecoveryPrevBootToOS = "on"
		repo := &forceNetbootFakeRepo{stored: n}
		if err := guardService(repo).ForceNetbootAfterDiskErase(ctx, account, n.ID); err != nil {
			t.Fatal(err)
		}
		if repo.stored.BootToOS == nil || *repo.stored.BootToOS {
			t.Fatalf("boot_to_os = %v, want an explicit recovery target", repo.stored.BootToOS)
		}
		if repo.stored.RecoveryRevertPending || repo.stored.RecoveryPrevBootToOS != "" {
			t.Fatal("the reboot-to-recovery one-shot survived; its restore would put the local OS back")
		}
	})

	t.Run("on its own OS: target kept", func(t *testing.T) {
		n := bootReportingNode(account, "sd", false)
		on := true
		n.BootToOS = &on
		repo := &forceNetbootFakeRepo{stored: n}
		if err := guardService(repo).ForceNetbootAfterDiskErase(ctx, account, n.ID); err != nil {
			t.Fatal(err)
		}
		if repo.stored.BootToOS == nil || !*repo.stored.BootToOS {
			t.Fatalf("boot_to_os = %v, want the local OS kept", repo.stored.BootToOS)
		}
	})
}
