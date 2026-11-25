package sql

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	nodesvc "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// Against real Postgres: a node deleted in account A is invisible to account
// B's list, and B's restore and forget answer not found and change nothing —
// even when B holds the same hardware serial. A's own restore still works.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test -p 1 ./pkg/webapp/infrastructure/database/sql/
func TestADeletedNodeCannotBeRestoredFromAnotherAccount(t *testing.T) {
	repo, svc := restoreAdmissionService(t, 0)
	ctx := context.Background()
	accountA, accountB := uuid.New(), uuid.New()
	const serial = "10000000feed0a0b"
	deleted := seedNode(t, repo, accountA, func(n *node.Node) {
		n.Status = node.StatusOffline
		n.Fingerprint = &node.HardwareFingerprint{HardwareSerial: serial}
	})
	if err := svc.Delete(ctx, accountA, uuid.New(), deleted.ID, false); err != nil {
		t.Fatalf("delete in A: %v", err)
	}

	if got, err := svc.ListDeleted(ctx, accountB); err != nil || len(got) != 0 {
		t.Errorf("B's list = %d records, err %v; want none of A's", len(got), err)
	}
	if _, err := svc.RestoreDeleted(ctx, accountB, uuid.New(), deleted.ID); !errors.Is(err, app_errors.ErrNotFound) {
		t.Errorf("B's restore of A's record: err = %v, want not found", err)
	}
	if err := svc.ForgetDeleted(ctx, accountB, uuid.New(), deleted.ID); !errors.Is(err, app_errors.ErrNotFound) {
		t.Errorf("B's forget of A's record: err = %v, want not found", err)
	}
	if _, err := repo.GetByID(ctx, accountB, deleted.ID); err == nil {
		t.Fatal("a node row exists in B under A's deleted id")
	}

	got, err := svc.ListDeleted(ctx, accountA)
	if err != nil || len(got) != 1 || got[0].RestoredAt != nil {
		t.Fatalf("A's record after B's attempts: %+v, err %v; want it untouched", got, err)
	}
	restored, err := svc.RestoreDeleted(ctx, accountA, uuid.New(), deleted.ID)
	if err != nil || restored.AccountID != accountA {
		t.Fatalf("A's own restore: %+v, %v", restored, err)
	}
}

// The board is adopted again as a new node in the same account: the list
// names that node and the restore refuses, typed, writing nothing.
func TestRestoreRefusesASerialHeldByAnotherNodeOfTheSameAccount(t *testing.T) {
	repo, svc := restoreAdmissionService(t, 0)
	ctx := context.Background()
	account := uuid.New()
	const serial = "10000000feed0c0d"
	deleted := seedNode(t, repo, account, func(n *node.Node) {
		n.Status = node.StatusOffline
		n.Fingerprint = &node.HardwareFingerprint{HardwareSerial: serial}
	})
	if err := svc.Delete(ctx, account, uuid.New(), deleted.ID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	readopted := seedNode(t, repo, account, func(n *node.Node) {
		n.Name = "bench-pi"
		n.Fingerprint = &node.HardwareFingerprint{HardwareSerial: serial}
	})

	got, err := svc.ListDeleted(ctx, account)
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %+v, %v", got, err)
	}
	if h := got[0].SerialBoundInAccount; h == nil || h.ID != readopted.ID || h.Name != "bench-pi" {
		t.Errorf("SerialBoundInAccount = %+v, want the re-adopted node %s", h, readopted.ID)
	}
	if got[0].SerialBoundElsewhere {
		t.Error("an own-account holder was reported as another account's")
	}

	_, err = svc.RestoreDeleted(ctx, account, uuid.New(), deleted.ID)
	var refusal *nodesvc.SerialBoundInAccountError
	if !errors.As(err, &refusal) || refusal.HolderID != readopted.ID {
		t.Fatalf("restore: err = %v, want the same-account refusal naming %s", err, readopted.ID)
	}
	if _, err := repo.GetByID(ctx, account, deleted.ID); err == nil {
		t.Error("the refused restore inserted the row")
	}
}
