package sql

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// The per-account audit retention against a real Postgres: the install
// default sweeps only accounts without their own value, an account's own
// value sweeps only that account and only while it is still stored, and the
// column's CHECK refuses an out-of-range write.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/ -run AuditRetention

func auditRetentionDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	for _, table := range []string{"audit_events", "accounts"} {
		if err := db.Exec("DROP TABLE IF EXISTS " + table + " CASCADE").Error; err != nil {
			t.Fatalf("dropping %s: %v", table, err)
		}
	}
	if err := NewAccountRepository(db).Setup(); err != nil {
		t.Fatalf("migrating accounts: %v", err)
	}
	if err := NewAuditEventRepository(db).Setup(); err != nil {
		t.Fatalf("migrating audit_events: %v", err)
	}
	return db
}

func TestAuditRetentionDeletesPerAccount(t *testing.T) {
	db := auditRetentionDB(t)
	ctx := context.Background()
	accounts := NewAccountRepository(db)
	audits := NewAuditEventRepository(db)

	onDefault, withOwn := account.NewAccount("default"), account.NewAccount("own")
	for _, a := range []*account.Account{onDefault, withOwn} {
		if err := accounts.Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("UPDATE accounts SET audit_retention_days = 30 WHERE id = ?", withOwn.ID).Error; err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * 24 * time.Hour)
	for _, id := range []uuid.UUID{onDefault.ID, withOwn.ID} {
		if err := audits.Create(ctx, &audit_event.AuditEvent{ID: uuid.New(), AccountID: id, ActorKind: "user",
			Action: "node.delete", TargetType: "node", TargetID: "n", CreatedAt: old}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(id uuid.UUID) int64 {
		var n int64
		db.Model(&AuditEventModel{}).Where("account_id = ?", id).Count(&n)
		return n
	}

	// An event whose account no longer exists is governed by the default too.
	orphan := uuid.New()
	if err := audits.Create(ctx, &audit_event.AuditEvent{ID: uuid.New(), AccountID: orphan, ActorKind: "user",
		Action: "node.delete", TargetType: "node", TargetID: "n", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	governed, err := audits.ListInstallDefaultAuditAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(governed) != 2 || !slices.Contains(governed, onDefault.ID) || !slices.Contains(governed, orphan) {
		t.Fatalf("install-default accounts = %v, want the default account and the orphaned id", governed)
	}
	for _, id := range governed {
		if _, err := audits.DeleteAccountOlderThanAtInstallDefault(ctx, id, time.Now(), 500); err != nil {
			t.Fatal(err)
		}
	}
	// Never at the default for an account that carries its own value.
	if n, err := audits.DeleteAccountOlderThanAtInstallDefault(ctx, withOwn.ID, time.Now(), 500); err != nil || n != 0 {
		t.Fatalf("install-default delete of an overridden account removed %d (%v), want 0", n, err)
	}
	if count(onDefault.ID) != 0 || count(orphan) != 0 || count(withOwn.ID) != 1 {
		t.Fatalf("install-default sweep: default=%d own=%d, want 0 and 1", count(onDefault.ID), count(withOwn.ID))
	}

	overrides, err := audits.ListAuditRetentionOverrides(ctx)
	if err != nil || len(overrides) != 1 || overrides[0].AccountID != withOwn.ID || overrides[0].Days != 30 {
		t.Fatalf("overrides = %+v, %v", overrides, err)
	}
	// A value changed since it was listed is not applied with the old one.
	if n, err := audits.DeleteAccountOlderThan(ctx, repository.AuditRetentionOverride{AccountID: withOwn.ID, Days: 31}, time.Now(), 500); err != nil || n != 0 {
		t.Fatalf("stale override deleted %d (%v), want 0", n, err)
	}
	if n, err := audits.DeleteAccountOlderThan(ctx, overrides[0], time.Now(), 500); err != nil || n != 1 {
		t.Fatalf("own-value sweep deleted %d (%v), want 1", n, err)
	}

	if err := db.Exec("UPDATE accounts SET audit_retention_days = 99999 WHERE id = ?", withOwn.ID).Error; err == nil {
		t.Error("the CHECK constraint accepted a retention above the ceiling")
	}
	if err := db.Exec("UPDATE accounts SET audit_retention_days = -1 WHERE id = ?", withOwn.ID).Error; err == nil {
		t.Error("the CHECK constraint accepted a negative retention")
	}
}
