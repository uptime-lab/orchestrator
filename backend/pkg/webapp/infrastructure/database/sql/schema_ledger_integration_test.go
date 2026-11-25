package sql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
)

// The schema-version ledger against a real Postgres: the tolerated case is a
// SQLSTATE, and "no DDL ran before the refusal" is a property of a database,
// so neither can be proven by a fake.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/ -run Ledger
//
// Each test gets its OWN Postgres schema (search_path), so "a fresh database"
// is literally empty and nothing another test in the package left behind can
// make a refusal look clean.
func ledgerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	name := "ledger_it_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA %s`, name)).Error; err != nil {
		t.Fatalf("creating schema: %v", err)
	}
	// The production connection's flags (postgres.NewDatabaseConnection):
	// prepared statements and error translation are both on, and the 42P01
	// match must survive both.
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+name), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		PrepareStmt:            true,
		SkipDefaultTransaction: true,
		TranslateError:         true,
	})
	if err != nil {
		t.Fatalf("connecting with search_path: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = admin.Exec(fmt.Sprintf(`DROP SCHEMA %s CASCADE`, name)).Error
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func tableExists(t *testing.T, db *gorm.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := db.Raw(`SELECT to_regclass(?) IS NOT NULL`, table).Scan(&exists).Error; err != nil {
		t.Fatalf("checking %s: %v", table, err)
	}
	return exists
}

func ledgerRows(t *testing.T, db *gorm.DB) []SchemaVersionModel {
	t.Helper()
	var rows []SchemaVersionModel
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("reading ledger: %v", err)
	}
	return rows
}

// bootAgainst replays main()'s sequence around the migrations — read and
// decide first, then the ledger table and this build's unfinished row, then a
// repository Setup() (instance_heartbeats stands for all of them: it is the
// table "a later Setup() would create"), then the row marked migrated.
// main()'s order itself is held by TestSchemaLedgerCheckRunsBeforeAnySetup in
// cmd/server.
func bootAgainst(t *testing.T, db *gorm.DB, build int, revision string) error {
	t.Helper()
	return bootAgainstFailing(t, db, build, revision, nil)
}

// bootAgainstFailing is bootAgainst whose migration step fails with
// migrationErr when it is non-nil — after the product DDL ran, the way a
// Setup() chain that fails part-way leaves a database.
func bootAgainstFailing(t *testing.T, db *gorm.DB, build int, revision string, migrationErr error) error {
	t.Helper()
	ctx := context.Background()
	ledgerRepo := NewSchemaLedgerRepository(db)
	before, err := ledgerRepo.Read(ctx)
	if err != nil {
		return err
	}
	if _, err := schema.Check(before, build); err != nil {
		return err
	}
	if err := ledgerRepo.Setup(); err != nil {
		t.Fatalf("ledger setup: %v", err)
	}
	if _, err := ledgerRepo.Record(ctx, schema.NewEntry(before, build, revision)); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := NewInstanceHeartbeatRepository(db).Setup(); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if migrationErr != nil {
		return migrationErr
	}
	if err := ledgerRepo.MarkMigrated(ctx, build, revision); err != nil {
		t.Fatalf("mark migrated: %v", err)
	}
	return nil
}

func TestLedgerFreshDatabaseIsMigratedAndRecorded(t *testing.T) {
	db := ledgerTestDB(t)
	l, err := NewSchemaLedgerRepository(db).Read(context.Background())
	if err != nil {
		t.Fatalf("a missing table must read as no ledger, got %v", err)
	}
	if l.Present {
		t.Fatalf("ledger reads as present on an empty database: %+v", l)
	}
	if err := bootAgainst(t, db, 1, "rev-a"); err != nil {
		t.Fatalf("fresh boot refused: %v", err)
	}
	rows := ledgerRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.SchemaVersion != 1 || r.BuildRevision != "rev-a" || r.PreviousVersion != 0 || !r.Migrated || r.AppliedAt.IsZero() {
		t.Fatalf("row = %+v", r)
	}
	if !tableExists(t, db, "instance_heartbeats") {
		t.Fatal("the migration did not run")
	}
}

// failedStatementRecorder captures every statement gorm reports as failed —
// exactly what the production logger turns into an ERROR line.
type failedStatementRecorder struct {
	logger.Interface
	mu     sync.Mutex
	failed []string
}

func (r *failedStatementRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	if err == nil {
		return
	}
	sql, _ := fc()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, sql+": "+err.Error())
}

// Reading the ledger on a new install is the normal first boot, not a fault:
// it must not issue a statement that fails, or every fresh install logs an
// ERROR before it has done anything.
func TestLedgerReadOnAnEmptyDatabaseIssuesNoFailingStatement(t *testing.T) {
	db := ledgerTestDB(t)
	rec := &failedStatementRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	l, err := NewSchemaLedgerRepository(db.Session(&gorm.Session{Logger: rec})).Read(context.Background())
	if err != nil || l.Present {
		t.Fatalf("empty database read as %+v, %v; want absent, no error", l, err)
	}
	if len(rec.failed) != 0 {
		t.Fatalf("reading an absent ledger issued failing statements: %q", rec.failed)
	}
}

func TestLedgerSameVersionAddsARowPerBuildNeverPerBoot(t *testing.T) {
	db := ledgerTestDB(t)
	for i := 0; i < 3; i++ {
		if err := bootAgainst(t, db, 1, "rev-a"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(ledgerRows(t, db)); n != 1 {
		t.Fatalf("three boots of one build wrote %d rows, want 1", n)
	}
	for i := 0; i < 2; i++ {
		if err := bootAgainst(t, db, 1, "rev-b"); err != nil {
			t.Fatal(err)
		}
	}
	rows := ledgerRows(t, db)
	if len(rows) != 2 {
		t.Fatalf("a second build at the same version: %d rows, want 2", len(rows))
	}
	if rows[1].BuildRevision != "rev-b" || !rows[1].Migrated || rows[1].PreviousVersion != 1 {
		t.Fatalf("same-version row = %+v, want rev-b, migrated (finished), previous 1", rows[1])
	}
}

func TestLedgerUpgradeRecordsThePreviousVersion(t *testing.T) {
	db := ledgerTestDB(t)
	if err := bootAgainst(t, db, 1, "rev-a"); err != nil {
		t.Fatal(err)
	}
	if err := bootAgainst(t, db, 2, "rev-c"); err != nil {
		t.Fatalf("upgrade refused: %v", err)
	}
	rows := ledgerRows(t, db)
	last := rows[len(rows)-1]
	if last.SchemaVersion != 2 || last.PreviousVersion != 1 || !last.Migrated {
		t.Fatalf("upgrade row = %+v", last)
	}
}

func TestLedgerNewerSchemaIsRefusedWithTheDatabaseUntouched(t *testing.T) {
	db := ledgerTestDB(t)
	// A database a newer release migrated: its ledger and nothing this build's
	// migrations would create.
	if err := NewSchemaLedgerRepository(db).Setup(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSchemaLedgerRepository(db).Record(context.Background(),
		schema.Entry{SchemaVersion: 5, BuildRevision: "rev-new", Migrated: true}); err != nil {
		t.Fatal(err)
	}

	err := bootAgainst(t, db, 4, "rev-old")
	var newer *schema.NewerSchemaError
	if !errors.As(err, &newer) || newer.Stored != 5 || newer.Build != 4 {
		t.Fatalf("err = %v, want a refusal naming 5 and 4", err)
	}
	if tableExists(t, db, "instance_heartbeats") {
		t.Fatal("a refused boot created a table: DDL ran before the refusal")
	}
	if rows := ledgerRows(t, db); len(rows) != 1 || rows[0].SchemaVersion != 5 {
		t.Fatalf("a refused boot changed the ledger: %+v", rows)
	}
}

func TestLedgerPreLedgerDatabaseIsTreatedAsFresh(t *testing.T) {
	db := ledgerTestDB(t)
	// Tables from a release that predates the ledger, but no ledger.
	if err := NewInstanceHeartbeatRepository(db).Setup(); err != nil {
		t.Fatal(err)
	}
	l, err := NewSchemaLedgerRepository(db).Read(context.Background())
	if err != nil || l.Present {
		t.Fatalf("pre-ledger database read as %+v, %v; want absent, no error", l, err)
	}
	if err := bootAgainst(t, db, 1, "rev-a"); err != nil {
		t.Fatalf("pre-ledger boot refused: %v", err)
	}
	if rows := ledgerRows(t, db); len(rows) != 1 || rows[0].PreviousVersion != 0 || !rows[0].Migrated {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestLedgerReadErrorIsNotAnAbsentLedger(t *testing.T) {
	db := ledgerTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	_ = sqlDB.Close()
	l, err := NewSchemaLedgerRepository(db).Read(context.Background())
	if err == nil {
		t.Fatalf("a read on a closed pool returned %+v and no error: an unreadable ledger must fail closed", l)
	}
}

// A newer build whose migrations fail
// part-way has already applied DDL; before the intent row it had left no
// ledger row, so the older build it was rolled back to started on the
// half-migrated tables. Now its unfinished row refuses the older build, and
// the same build reads the row as "migrate again".
func TestLedgerAFailedMigrationLeavesARowThatRefusesTheOlderBuild(t *testing.T) {
	db := ledgerTestDB(t)
	if err := bootAgainst(t, db, 1, "rev-old"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("a Setup() failed part-way")
	if err := bootAgainstFailing(t, db, 2, "rev-new", failure); !errors.Is(err, failure) {
		t.Fatalf("err = %v, want the injected migration failure", err)
	}

	l, err := NewSchemaLedgerRepository(db).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !l.Present || l.Highest != 2 || l.Complete {
		t.Fatalf("ledger after a failed migration = %+v, want version 2, unfinished", l)
	}
	err = bootAgainst(t, db, 1, "rev-old")
	var newer *schema.NewerSchemaError
	if !errors.As(err, &newer) || newer.Stored != 2 || newer.Build != 1 {
		t.Fatalf("the older build was not refused on a half-migrated database: %v", err)
	}
	if d, _ := schema.Check(l, 2); d != schema.Migrate {
		t.Fatalf("the same build reads its unfinished row as %v, want migrate", d)
	}

	// The same build boots again, its migrations finish, and the row says so.
	if err := bootAgainst(t, db, 2, "rev-new"); err != nil {
		t.Fatalf("the retry was refused: %v", err)
	}
	l, err = NewSchemaLedgerRepository(db).Read(context.Background())
	if err != nil || !l.Complete || l.Highest != 2 {
		t.Fatalf("after the retry the ledger is %+v, %v; want version 2, finished", l, err)
	}
	if rows := ledgerRows(t, db); len(rows) != 2 {
		t.Fatalf("the retry added a row: %+v", rows)
	}
}
