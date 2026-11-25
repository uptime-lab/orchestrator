package node

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// A system-info report reads the node row, builds the report's columns and
// writes them back. An operator column write that commits between that read
// and that write must survive it: the report owns its own columns, never the
// operator's.

// reportRaceRepo holds "the database row". Its first GetByID hands out a copy
// and then applies an operator write to the row, which is the interleaving
// under test. Update is the full-row save; UpdateSystemReport writes only the
// columns it is told to.
type reportRaceRepo struct {
	repository.NodeRepository
	row            node.Node
	reads          int
	operatorWrite  func(*node.Node)
	reportColumns  []string
	revertAsked    bool
	fullRowUpdates int
}

func (r *reportRaceRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*node.Node, error) {
	cp := r.row
	r.reads++
	if r.reads == 1 && r.operatorWrite != nil {
		r.operatorWrite(&r.row)
	}
	return &cp, nil
}

func (r *reportRaceRepo) Update(_ context.Context, n *node.Node) error {
	r.fullRowUpdates++
	r.row = *n
	return nil
}

func (r *reportRaceRepo) UpdateSystemReport(_ context.Context, n *node.Node, columns []string, completeRecoveryRevert bool) (bool, error) {
	r.reportColumns = columns
	r.revertAsked = completeRecoveryRevert
	for _, c := range columns {
		switch c {
		case "system_info":
			r.row.SystemInfo = n.SystemInfo
		case "storage_devices":
			r.row.StorageDevices = n.StorageDevices
		case "last_boot_at":
			r.row.LastBootAt = n.LastBootAt
		case "bootloader_version":
			r.row.BootloaderVersion = n.BootloaderVersion
		case "bootloader_timestamp":
			r.row.BootloaderTimestamp = n.BootloaderTimestamp
		case "hostname":
			r.row.Hostname = n.Hostname
		case "ipv4":
			r.row.IPv4 = n.IPv4
		}
	}
	if completeRecoveryRevert && r.row.RecoveryRevertPending && r.row.RecoveryPrevBootToOS == n.RecoveryPrevBootToOS {
		r.row.BootToOS = n.BootToOS
		r.row.RecoveryRevertPending = false
		r.row.RecoveryPrevBootToOS = ""
		return true, nil
	}
	return false, nil
}

func reportRaceService(repo *reportRaceRepo) *Service {
	return &Service{nodeRepo: repo, logger: slog.New(slog.DiscardHandler)}
}

func TestSystemReportDoesNotRevertAConcurrentOperatorWrite(t *testing.T) {
	on, off := true, false
	repo := &reportRaceRepo{
		row: node.Node{
			ID: uuid.New(), AccountID: uuid.New(),
			ApprovalStatus: node.ApprovalStatusApproved, Status: node.StatusOnline,
			SSHPasswordAuth: &on,
			Hostname:        "old-host",
		},
		operatorWrite: func(n *node.Node) {
			n.SSHPasswordAuth = &off
			n.BootToOS = &off
			n.Notes = "operator note"
		},
	}

	err := reportRaceService(repo).UpdateSystemInfo(context.Background(), repo.row.ID, repo.row.AccountID, &node.SystemInfo{
		Network: &node.NetworkInfo{Hostname: "new-host", IP: "192.0.2.10"},
	})
	if err != nil {
		t.Fatalf("UpdateSystemInfo: %v", err)
	}

	if repo.row.SSHPasswordAuth == nil || *repo.row.SSHPasswordAuth {
		t.Errorf("ssh_password_auth reverted to the value read before the operator's write")
	}
	if repo.row.BootToOS == nil || *repo.row.BootToOS {
		t.Errorf("boot_to_os reverted to the value read before the operator's write")
	}
	if repo.row.Notes != "operator note" {
		t.Errorf("notes reverted to %q", repo.row.Notes)
	}
	if repo.fullRowUpdates != 0 {
		t.Errorf("the report did %d full-row save(s); it must write only the columns it owns", repo.fullRowUpdates)
	}
	if repo.row.Hostname != "new-host" || repo.row.IPv4 != "192.0.2.10" || repo.row.SystemInfo == nil {
		t.Errorf("the report's own columns were not written: hostname=%q ipv4=%q system_info=%v",
			repo.row.Hostname, repo.row.IPv4, repo.row.SystemInfo)
	}
}

// The reboot-to-recovery completion rides the same narrow write, and it is
// asked for with the previous boot target as read, so the repository can
// refuse it on a row whose pending revert changed in between.
func TestSystemReportCompletesRecoveryRevertThroughTheConditionalWrite(t *testing.T) {
	off := false
	repo := &reportRaceRepo{
		row: node.Node{
			ID: uuid.New(), AccountID: uuid.New(),
			ApprovalStatus: node.ApprovalStatusApproved, Status: node.StatusOnline,
			BootToOS:              &off,
			RecoveryRevertPending: true,
			RecoveryPrevBootToOS:  "on",
		},
	}

	err := reportRaceService(repo).UpdateSystemInfo(context.Background(), repo.row.ID, repo.row.AccountID, &node.SystemInfo{
		Hardware: &node.HardwareInfo{Boot: &node.BootInfo{CurrentBootDevice: "http"}},
	})
	if err != nil {
		t.Fatalf("UpdateSystemInfo: %v", err)
	}
	if !repo.revertAsked {
		t.Fatal("an http-boot report on a node with a pending revert did not ask to complete it")
	}
	if repo.row.RecoveryRevertPending || repo.row.BootToOS == nil || !*repo.row.BootToOS {
		t.Errorf("revert not completed: pending=%v boot_to_os=%v", repo.row.RecoveryRevertPending, repo.row.BootToOS)
	}
}
