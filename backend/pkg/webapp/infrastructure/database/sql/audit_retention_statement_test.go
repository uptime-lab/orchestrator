package sql

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
)

func captureDelete(t *testing.T, db *gorm.DB) *string {
	t.Helper()
	var captured string
	if err := db.Callback().Delete().After("gorm:delete").Register("test:capture", func(tx *gorm.DB) {
		captured = tx.Statement.SQL.String()
	}); err != nil {
		t.Fatal(err)
	}
	return &captured
}

// The install-default sweep deletes one account's rows at a time, bounded, and
// only while that account still carries no retention of its own — read in the
// same statement. A statement excluding the overridden accounts with NOT IN
// re-read their whole history every tick.
func TestInstallDefaultAuditDeleteIsPerAccountAndGuarded(t *testing.T) {
	db := dryRunDB(t)
	stmt := captureDelete(t, db)
	_, _ = NewAuditEventRepository(db).DeleteAccountOlderThanAtInstallDefault(context.Background(), uuid.New(), time.Now(), 500)
	for _, want := range []string{
		`DELETE FROM "audit_events"`,
		`account_id = $`,
		`NOT EXISTS (SELECT 1 FROM "accounts" WHERE id = $`,
		`audit_retention_days IS NOT NULL`,
		` LIMIT $`,
	} {
		if !strings.Contains(*stmt, want) {
			t.Errorf("statement lacks %q:\n%s", want, *stmt)
		}
	}
	if strings.Contains(*stmt, "NOT IN") {
		t.Errorf("statement still excludes accounts with NOT IN:\n%s", *stmt)
	}
}

// An account's own sweep is bounded, scoped to that account, and applies
// only while the stored value is the one the cutoff came from.
func TestAccountAuditDeleteIsScopedAndGuarded(t *testing.T) {
	db := dryRunDB(t)
	stmt := captureDelete(t, db)
	_, _ = NewAuditEventRepository(db).DeleteAccountOlderThan(context.Background(),
		repository.AuditRetentionOverride{AccountID: uuid.New(), Days: 30}, time.Now(), 500)
	for _, want := range []string{
		`DELETE FROM "audit_events"`,
		`account_id = $`,
		`EXISTS (SELECT 1 FROM "accounts" WHERE id = $`,
		`audit_retention_days = $`,
		` LIMIT $`,
	} {
		if !strings.Contains(*stmt, want) {
			t.Errorf("statement lacks %q:\n%s", want, *stmt)
		}
	}
}
