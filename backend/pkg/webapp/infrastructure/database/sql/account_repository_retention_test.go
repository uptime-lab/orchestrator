package sql

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
)

// The audit retention column is set by instance administration, never by
// the tenant API: the account Update every tenant edit goes through must not
// write it, or an edit made from a copy read earlier puts the old value back.
func TestAccountUpdateNeverWritesTheAuditRetention(t *testing.T) {
	db := dryRunDB(t)
	var captured string
	if err := db.Callback().Update().After("gorm:update").Register("test:capture", func(tx *gorm.DB) {
		captured = tx.Statement.SQL.String()
	}); err != nil {
		t.Fatal(err)
	}
	days := 30
	_ = NewAccountRepository(db).Update(context.Background(), &account.Account{
		ID: uuid.New(), Name: "renamed", AuditRetentionDays: &days,
	})
	if !strings.HasPrefix(captured, "UPDATE ") {
		t.Fatalf("no UPDATE captured: %q", captured)
	}
	if strings.Contains(captured, accountAuditRetentionColumn) {
		t.Errorf("the account update writes the audit retention column: %s", captured)
	}
	if !strings.Contains(captured, `"name"=`) {
		t.Errorf("the account update no longer writes the name: %s", captured)
	}
}

// The per-account device-limit override is set by instance administration,
// like the retention: the tenant-facing account Update must never write it,
// or a rename from a copy read before a subscription was granted would
// clear the subscription.
func TestAccountUpdateNeverWritesTheDeviceLimitOverride(t *testing.T) {
	db := dryRunDB(t)
	var captured string
	if err := db.Callback().Update().After("gorm:update").Register("test:capture", func(tx *gorm.DB) {
		captured = tx.Statement.SQL.String()
	}); err != nil {
		t.Fatal(err)
	}
	_ = NewAccountRepository(db).Update(context.Background(), &account.Account{ID: uuid.New(), Name: "renamed"})
	if !strings.HasPrefix(captured, "UPDATE ") {
		t.Fatalf("no UPDATE captured: %q", captured)
	}
	for _, col := range accountDeviceLimitColumns {
		if strings.Contains(captured, `"`+col+`"`) {
			t.Errorf("the account update writes %s: %s", col, captured)
		}
	}
}
