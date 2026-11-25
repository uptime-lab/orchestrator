package sql

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// deadPool satisfies gorm.ConnPool so a dialector can be initialised with no
// database behind it. Every statement below is built in DryRun mode, so nothing
// is ever executed against it.
type deadPool struct{}

func (deadPool) PrepareContext(context.Context, string) (*sql.Stmt, error) {
	return nil, errors.New("dry run: no database")
}

func (deadPool) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, errors.New("dry run: no database")
}

func (deadPool) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, errors.New("dry run: no database")
}

func (deadPool) QueryRowContext(context.Context, string, ...interface{}) *sql.Row {
	return nil
}

func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: deadPool{}}),
		&gorm.Config{DryRun: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("opening a dry-run session: %v", err)
	}
	return db
}

// The repositories replaced gorm's Save with updateAllColumns wholesale, so the
// statement it builds has to be the same one — every column, zero values
// included, honouring Omit — or a dozen update paths silently changed meaning.
// Save's extra behaviour is exactly the part being dropped: it re-runs the write
// as an ON CONFLICT insert when the UPDATE matches nothing, which DryRun cannot
// reach (that fallback is covered against real Postgres in
// workflow_execution_write_integration_test.go).
func TestUpdateAllColumnsBuildsTheSameStatementAsSave(t *testing.T) {
	db := dryRunDB(t)
	id, accountID := uuid.New(), uuid.New()
	updatedAt := time.Now()

	newModel := func() *WorkflowExecutionModel {
		return &WorkflowExecutionModel{ID: id, AccountID: accountID, Status: "running", UpdatedAt: updatedAt}
	}

	saved := db.Session(&gorm.Session{DryRun: true}).
		Where("id = ? AND account_id = ?", id, accountID).
		Save(newModel()).Statement.SQL.String()
	updated := updateAllColumns(db.Session(&gorm.Session{DryRun: true}).
		Where("id = ? AND account_id = ?", id, accountID), newModel()).Statement.SQL.String()

	if updated != saved {
		t.Errorf("statements differ:\n  save:    %s\n  updated: %s", saved, updated)
	}
	if !strings.HasPrefix(updated, "UPDATE ") {
		t.Errorf("statement is not an UPDATE: %s", updated)
	}
	// Zero-valued columns must still be written: several callers clear a pointer
	// field (an OU assignment, a completion timestamp) and rely on the write
	// carrying the emptiness.
	for _, column := range []string{`"completed_at"=`, `"started_at"=`, `"node_count"=`, `"ou_id"=`} {
		if !strings.Contains(updated, column) {
			t.Errorf("column %s is not written, so clearing it would silently do nothing: %s", column, updated)
		}
	}
}

// Omit still wins over the all-columns select — the ssh_key, master_token,
// node_file and node update paths each depend on it to leave one stored column
// (an encrypted private key, a provisioning password, the denormalized account,
// the boot-failsafe counters) exactly as it is.
func TestUpdateAllColumnsHonoursOmit(t *testing.T) {
	db := dryRunDB(t)
	id := uuid.New()

	stmt := updateAllColumns(db.Session(&gorm.Session{DryRun: true}).
		Where("id = ?", id).
		Omit("private_key_encrypted"),
		&SSHKeyModel{ID: id, AccountID: uuid.New(), Alias: "connection key"}).Statement.SQL.String()

	if strings.Contains(stmt, "private_key_encrypted") {
		t.Errorf("the omitted column is in the statement: %s", stmt)
	}
	if !strings.Contains(stmt, `"alias"=`) {
		t.Errorf("Omit dropped more than it was asked to: %s", stmt)
	}
}
