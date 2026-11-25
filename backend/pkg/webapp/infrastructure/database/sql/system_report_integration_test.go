package sql

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// UpdateSystemReport against a real Postgres: a report's write names its
// columns, so an operator write that landed after the report read the row
// survives it, and the recovery-revert completion only lands on a row that
// still carries the revert the report saw.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

func TestUpdateSystemReportKeepsAConcurrentOperatorWrite(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	account := uuid.New()
	on := true
	seeded := seedNode(t, repo, account, func(n *node.Node) { n.SSHPasswordAuth = &on })

	// The report's read.
	stale, err := repo.GetByID(ctx, account, seeded.ID)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	// The operator's write, after it.
	if err := repo.UpdateNodeColumns(ctx, account, seeded.ID, map[string]interface{}{"ssh_password_auth": false}); err != nil {
		t.Fatalf("operator write: %v", err)
	}

	stale.Hostname = "reported-host"
	stale.SystemInfo = &node.SystemInfo{Network: &node.NetworkInfo{Hostname: "reported-host"}}
	if _, err := repo.UpdateSystemReport(ctx, stale, []string{"system_info", "hostname"}, false); err != nil {
		t.Fatalf("UpdateSystemReport: %v", err)
	}

	got, err := repo.GetByID(ctx, account, seeded.ID)
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	if got.SSHPasswordAuth == nil || *got.SSHPasswordAuth {
		t.Errorf("ssh_password_auth = %v, want the operator's false", got.SSHPasswordAuth)
	}
	if got.Hostname != "reported-host" || got.SystemInfo == nil {
		t.Errorf("the report's own columns were not written: hostname=%q system_info=%v", got.Hostname, got.SystemInfo)
	}
}

func TestUpdateSystemReportRefusesColumnsItDoesNotOwn(t *testing.T) {
	repo := deviceCapRepo(t)
	n := seedNode(t, repo, uuid.New(), nil)
	for _, c := range []string{"ssh_password_auth", "boot_to_os", "status", "approval_status", "unique_token", "ou_id"} {
		if _, err := repo.UpdateSystemReport(context.Background(), n, []string{c}, false); err == nil {
			t.Errorf("UpdateSystemReport wrote %s, which a report does not own", c)
		}
	}
}

func TestUpdateSystemReportCompletesOnlyTheRevertItSaw(t *testing.T) {
	repo := deviceCapRepo(t)
	ctx := context.Background()
	account := uuid.New()
	off := false
	seeded := seedNode(t, repo, account, func(n *node.Node) {
		n.BootToOS = &off
		n.RecoveryRevertPending = true
		n.RecoveryPrevBootToOS = "on"
	})

	// A report that saw a different pending revert than the row now holds.
	other, _ := repo.GetByID(ctx, account, seeded.ID)
	other.RecoveryPrevBootToOS = "inherit"
	other.BootToOS = nil
	if reverted, err := repo.UpdateSystemReport(ctx, other, []string{"system_info"}, true); err != nil || reverted {
		t.Fatalf("a revert the row no longer carries: reverted=%v err=%v, want false/nil", reverted, err)
	}
	if got, _ := repo.GetByID(ctx, account, seeded.ID); !got.RecoveryRevertPending || got.BootToOS == nil || *got.BootToOS {
		t.Fatalf("the row's pending revert was touched: pending=%v boot_to_os=%v", got.RecoveryRevertPending, got.BootToOS)
	}

	// The report that saw this revert completes it.
	same, _ := repo.GetByID(ctx, account, seeded.ID)
	on := true
	same.BootToOS = &on
	if reverted, err := repo.UpdateSystemReport(ctx, same, []string{"system_info"}, true); err != nil || !reverted {
		t.Fatalf("reverted=%v err=%v, want true/nil", reverted, err)
	}
	got, _ := repo.GetByID(ctx, account, seeded.ID)
	if got.RecoveryRevertPending || got.RecoveryPrevBootToOS != "" || got.BootToOS == nil || !*got.BootToOS {
		t.Errorf("revert not completed: pending=%v prev=%q boot_to_os=%v", got.RecoveryRevertPending, got.RecoveryPrevBootToOS, got.BootToOS)
	}
}
