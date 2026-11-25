package node_command

import (
	"context"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
)

// A successful erase_drive leaves the node on recovery, like a failed
// install: the disk it would boot may now be empty, and with the boot
// failsafe off nothing would serve it recovery again.
func TestASuccessfulEraseLeavesTheNodeOnRecovery(t *testing.T) {
	erase := cmd(node_command.SlugEraseDrive, node_command.StatusInProgress)
	repo := newLiveCommandRepo("", erase)
	svc, _, _ := unreachableService(&stubCommandRepo{})
	svc.repo = repo
	ns := &bootRuleNodeService{}
	svc.SetNodeService(ns)

	if err := svc.UpdateNodeCommandStatus(context.Background(), erase.ID, "success", 100, ""); err != nil {
		t.Fatalf("settling: %v", err)
	}
	if len(ns.forced) != 1 || ns.forced[0] != erase.NodeID {
		t.Fatalf("forced recovery for %v, want exactly the erased node %s", ns.forced, erase.NodeID)
	}
}

// A failed erase changed nothing the server can rely on either way, and the
// operator's boot target stands.
func TestAFailedEraseKeepsTheBootTarget(t *testing.T) {
	erase := cmd(node_command.SlugEraseDrive, node_command.StatusInProgress)
	repo := newLiveCommandRepo("", erase)
	svc, _, _ := unreachableService(&stubCommandRepo{})
	svc.repo = repo
	ns := &bootRuleNodeService{}
	svc.SetNodeService(ns)

	if err := svc.UpdateNodeCommandStatus(context.Background(), erase.ID, "failed", 0, "busy"); err != nil {
		t.Fatalf("settling: %v", err)
	}
	if len(ns.forced) != 0 {
		t.Fatalf("forced recovery %d times after a failed erase, want 0", len(ns.forced))
	}
}
