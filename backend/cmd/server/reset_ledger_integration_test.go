package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/schema"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/database/sql"
)

// The credential reset against a real Postgres: whether it asks the
// schema-version ledger before writing is a property of a database.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./cmd/server/ -run ResetLedger
//
// Each test gets its own Postgres schema (search_path).
func resetLedgerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	quiet := &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)}
	admin, err := gorm.Open(postgres.Open(dsn), quiet)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	name := "reset_it_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA %s`, name)).Error; err != nil {
		t.Fatalf("creating schema: %v", err)
	}
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+name), &gorm.Config{
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
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
	for _, setup := range []func() error{
		sql.NewUserRepository(db).Setup,
		sql.NewTwoFactorRepository(db).Setup,
		sql.NewRefreshTokenRepository(db).Setup,
		sql.NewPasswordResetRepository(db).Setup,
		sql.NewAccountMemberRepository(db).Setup,
		sql.NewAuditEventRepository(db).Setup,
		sql.NewSchemaLedgerRepository(db).Setup,
	} {
		if err := setup(); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	return db
}

type resetRowState struct {
	Hash       string `gorm:"column:hashed_password"`
	Generation int64  `gorm:"column:credential_generation"`
}

func seedResetUser(t *testing.T, db *gorm.DB, ledgerVersion int) (*user.User, resetRowState) {
	t.Helper()
	usr := user.NewUser("owner@example.com", "Owner", "$2a$04$abcdefghijklmnopqrstuuJ4n0Zr3qVZr0aYQ8Yb8dV0g6YbE5W2u")
	if err := sql.NewUserRepository(db).Create(context.Background(), usr); err != nil {
		t.Fatalf("seeding the user: %v", err)
	}
	if _, err := sql.NewSchemaLedgerRepository(db).Record(context.Background(), schema.Entry{
		SchemaVersion: ledgerVersion, BuildRevision: "seeded", Migrated: true,
	}); err != nil {
		t.Fatalf("seeding the ledger: %v", err)
	}
	return usr, readResetRow(t, db, usr)
}

func readResetRow(t *testing.T, db *gorm.DB, usr *user.User) resetRowState {
	t.Helper()
	var st resetRowState
	if err := db.Raw(`SELECT hashed_password, credential_generation FROM users WHERE id = ?`, usr.ID).Scan(&st).Error; err != nil {
		t.Fatalf("reading the user: %v", err)
	}
	return st
}

// An older build run against a schema a newer one migrated is refused with
// the boot's own text, before a single write.
func TestResetLedgerRefusesANewerSchemaAndWritesNothing(t *testing.T) {
	db := resetLedgerTestDB(t)
	usr, before := seedResetUser(t, db, schema.Version+1)

	var stdout, stderr bytes.Buffer
	code := resetAgainst(context.Background(), db, schema.Version,
		resetRequest{mode: resetPassword, email: "owner@example.com", passwordStdin: true},
		"a-chosen-password", quietLogger(), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr: %s", code, stderr.String())
	}
	want := (&schema.NewerSchemaError{Stored: schema.Version + 1, Build: schema.Version}).Error()
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("stderr %q does not carry the boot's refusal %q", stderr.String(), want)
	}
	if after := readResetRow(t, db, usr); after != before {
		t.Fatalf("the refused reset wrote the user row: %+v -> %+v", before, after)
	}
	var audits int64
	if err := db.Raw(`SELECT count(*) FROM audit_events`).Scan(&audits).Error; err != nil || audits != 0 {
		t.Fatalf("the refused reset wrote %d audit row(s) (err %v)", audits, err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout carried %q", stdout.String())
	}
}

// The same version proceeds and writes; the check is not a blanket refusal.
func TestResetLedgerProceedsOnTheSameVersion(t *testing.T) {
	db := resetLedgerTestDB(t)
	usr, before := seedResetUser(t, db, schema.Version)

	var stdout, stderr bytes.Buffer
	code := resetAgainst(context.Background(), db, schema.Version,
		resetRequest{mode: resetPassword, email: "owner@example.com", passwordStdin: true},
		"a-chosen-password", quietLogger(), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr: %s", code, stderr.String())
	}
	after := readResetRow(t, db, usr)
	if after.Hash == before.Hash || after.Generation <= before.Generation {
		t.Fatalf("the reset did not write: %+v -> %+v", before, after)
	}
}
