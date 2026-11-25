package sql

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_connect"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	nodesvc "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// A restore is not an admission. Against real Postgres, because the bypass
// it closes lived in a column DEFAULT: a restore that left approval_status
// unset got 'approved' from the database, so a row that never connected came
// back admitted, and its device then connected through the "already
// admitted" branch with no cap check.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test -p 1 ./pkg/webapp/infrastructure/database/sql/

func restoreAdmissionService(t *testing.T, cap int) (*NodeRepository, *nodesvc.Service) {
	t.Helper()
	repo := deviceCapRepo(t)
	if err := repo.db.Exec(`DROP TABLE IF EXISTS node_tombstones CASCADE`).Error; err != nil {
		t.Fatalf("dropping node_tombstones: %v", err)
	}
	tombstones := NewNodeTombstoneRepository(repo.db, passthroughEncryptor{})
	if err := tombstones.Setup(); err != nil {
		t.Fatalf("migrating node_tombstones: %v", err)
	}
	recorder, _ := audittest.New()
	svc := nodesvc.NewService(repo, nil, nil, nil, nil, nil, nil, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.SetNodeTombstoneRepo(tombstones)
	svc.SetDeviceCapResolver(fixedDeviceCap(cap))
	return repo, svc
}

func deleteAndRestore(t *testing.T, svc *nodesvc.Service, n *node.Node) *node.Node {
	t.Helper()
	ctx := context.Background()
	if err := svc.Delete(ctx, n.AccountID, uuid.New(), n.ID, false); err != nil {
		t.Fatalf("delete %s: %v", n.ID, err)
	}
	restored, err := svc.RestoreDeleted(ctx, n.AccountID, uuid.New(), n.ID)
	if err != nil {
		t.Fatalf("restore %s: %v", n.ID, err)
	}
	return restored
}

// Cap 2, two admitted devices, five operator-created placeholders. Add,
// Delete, Restore, then let the five devices connect. A restore that admitted
// what it restored would leave admitted=7 at cap 2 and let every connect
// pass; the restored rows are placeholders again, and each connect is an
// admission the full install refuses.
func TestRestoredPlaceholdersCannotConnectPastTheCap(t *testing.T) {
	const cap, placeholders = 2, 5
	repo, svc := restoreAdmissionService(t, cap)
	ctx := context.Background()
	account := uuid.New()
	for i := 0; i < cap; i++ {
		seedNode(t, repo, account, nil)
	}

	var restored []*node.Node
	for i := 0; i < placeholders; i++ {
		n := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusReadyForAdoption })
		restored = append(restored, deleteAndRestore(t, svc, n))
	}
	if count, _ := repo.CountAdmittedNodes(ctx); count != cap {
		t.Fatalf("after restoring %d placeholders the install holds %d admitted devices, want %d: the restore admitted them",
			placeholders, count, cap)
	}

	refused := 0
	for i, n := range restored {
		_, err := svc.Connect(ctx, node_connect.NodeConnectCommand{NodeID: n.ID, AccountID: account,
			UniqueToken: n.UniqueToken,
			Fingerprint: node.HardwareFingerprint{HardwareSerial: fmt.Sprintf("10000000feed%04x", i),
				MacAddress: fmt.Sprintf("02:00:00:00:01:%02x", i)}})
		var capErr *entitlement.DeviceCapError
		switch {
		case errors.As(err, &capErr):
			refused++
		case err != nil:
			t.Fatalf("connect %d: %v", i, err)
		}
	}
	count, err := repo.CountAdmittedNodes(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if refused != placeholders || count != cap {
		t.Errorf("%d restored placeholders connecting at a full cap of %d: %d refused, %d admitted; want all refused, %d admitted",
			placeholders, cap, refused, count, cap)
	}
}

// A rejected device stays rejected and a pending one pending: a restore
// neither overturns the operator's rejection nor skips the approval that is
// the pending device's admission.
func TestRestoreKeepsRejectedAndPendingDevicesOutOfTheCount(t *testing.T) {
	repo, svc := restoreAdmissionService(t, 2)
	ctx := context.Background()
	account := uuid.New()

	for _, approval := range []node.ApprovalStatus{node.ApprovalStatusRejected, node.ApprovalStatusPendingApproval} {
		n := seedNode(t, repo, account, func(n *node.Node) {
			n.Status = node.StatusOffline
			n.ApprovalStatus = approval
		})
		deleteAndRestore(t, svc, n)
		row, err := repo.GetByID(ctx, account, n.ID)
		if err != nil {
			t.Fatalf("reading the restored row: %v", err)
		}
		if row.ApprovalStatus != approval || row.IsAdmitted() {
			t.Errorf("a %s device restored as approval=%q status=%q admitted=%t; want %s and not admitted",
				approval, row.ApprovalStatus, row.Status, row.IsAdmitted(), approval)
		}
	}
	if count, _ := repo.CountAdmittedNodes(ctx); count != 0 {
		t.Errorf("restoring a rejected and a pending device admitted %d", count)
	}
}

// A device that WAS admitted comes back admitted while the install has a
// free slot, and is counted from the restore on, so the next admission sees
// it.
func TestRestoreOfAnAdmittedDeviceIsCounted(t *testing.T) {
	const cap = 2
	repo, svc := restoreAdmissionService(t, cap)
	ctx := context.Background()
	account := uuid.New()
	seedNode(t, repo, account, nil)
	admitted := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusOffline })

	restored := deleteAndRestore(t, svc, admitted)
	if !restored.IsAdmitted() {
		t.Fatalf("a device admitted at deletion was restored as status=%q approval=%q", restored.Status, restored.ApprovalStatus)
	}
	if count, _ := repo.CountAdmittedNodes(ctx); count != cap {
		t.Fatalf("admitted count after the restore = %d, want %d", count, cap)
	}

	// The next admission sees the restored row and is refused.
	waiting := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusReadyForAdoption })
	_, err := svc.Connect(ctx, node_connect.NodeConnectCommand{NodeID: waiting.ID, AccountID: account,
		UniqueToken: waiting.UniqueToken,
		Fingerprint: node.HardwareFingerprint{HardwareSerial: "10000000deadbeef", MacAddress: "02:00:00:00:02:01"}})
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Errorf("a first connect after restoring an admitted device into a full install: err = %v, want the cap refusal", err)
	}
}

// Delete an admitted device, admit another into its slot, restore the first:
// the restore is an admission and the full install refuses it, inserting
// nothing and leaving the record restorable. Without that, each cycle added
// one admitted device past the cap.
func TestRestoreOfAnAdmittedDeviceIsRefusedWhenItsSlotWasTaken(t *testing.T) {
	const cap = 2
	repo, svc := restoreAdmissionService(t, cap)
	ctx := context.Background()
	account := uuid.New()
	seedNode(t, repo, account, nil)
	deleted := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusOffline })

	if err := svc.Delete(ctx, account, uuid.New(), deleted.ID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	filler := seedNode(t, repo, account, func(n *node.Node) { n.Status = node.StatusOffline })

	_, err := svc.RestoreDeleted(ctx, account, uuid.New(), deleted.ID)
	var capErr *entitlement.DeviceCapError
	if !errors.As(err, &capErr) {
		t.Fatalf("restore into a full install: err = %v, want the cap refusal", err)
	}
	if count, _ := repo.CountAdmittedNodes(ctx); count != cap {
		t.Fatalf("admitted count after the refused restore = %d, want %d", count, cap)
	}
	if _, err := repo.GetByID(ctx, account, deleted.ID); err == nil {
		t.Error("the refused restore inserted the row")
	}

	if err := svc.Delete(ctx, account, uuid.New(), filler.ID, false); err != nil {
		t.Fatalf("delete filler: %v", err)
	}
	restored, err := svc.RestoreDeleted(ctx, account, uuid.New(), deleted.ID)
	if err != nil {
		t.Fatalf("restore once a slot is free: %v", err)
	}
	if !restored.IsAdmitted() || restored.CreatedAt.IsZero() {
		t.Errorf("restored admitted=%t created_at=%v; want admitted, with the stored timestamps", restored.IsAdmitted(), restored.CreatedAt)
	}
	if count, _ := repo.CountAdmittedNodes(ctx); count != cap {
		t.Errorf("admitted count after the restore = %d, want %d", count, cap)
	}
}
