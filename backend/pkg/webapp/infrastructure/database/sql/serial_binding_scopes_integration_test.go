package sql

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SerialBindingScopes is one aggregate query whose every interesting property —
// the GROUP BY spelling, bool_or over two accounts, the lower() match against a
// mixed-case stored serial — is invisible to the fake-backed service tests. It
// shipped once with Group("1"), which GORM renders as GROUP BY "1" (a quoted
// column name, not an ordinal); Postgres refused it, the caller failed open, and
// the Deleted Nodes flag would never have rendered anywhere. Hence a real
// database:
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

func serialScopesRepo(t *testing.T) *NodeRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS nodes CASCADE`).Error; err != nil {
		t.Fatalf("dropping nodes: %v", err)
	}
	repo := NewNodeRepository(db, passthroughEncryptor{})
	// Setup, not a bare AutoMigrate: the query is written against the
	// lower(...) expression index Setup creates, and the test should run over
	// the same shape production does.
	if err := repo.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return repo
}

func seedNodeWithSerial(t *testing.T, repo *NodeRepository, accountID uuid.UUID, serial string) {
	t.Helper()
	id := uuid.New()
	if err := repo.db.Exec(
		`INSERT INTO nodes (id, account_id, name, unique_token, status, approval_status, fingerprint, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'offline', 'approved', jsonb_build_object('hardware_serial', ?::text), now(), now())`,
		id, accountID, "serial-test-"+id.String()[:8], uuid.New().String(), serial).Error; err != nil {
		t.Fatalf("seeding node: %v", err)
	}
}

func TestSerialBindingScopesGroupsByTheNormalizedSerial(t *testing.T) {
	repo := serialScopesRepo(t)
	ctx := context.Background()
	mine := uuid.New()
	other := uuid.New()

	// One serial per situation the caller distinguishes.
	seedNodeWithSerial(t, repo, other, "1000000000000001") // held elsewhere only
	seedNodeWithSerial(t, repo, mine, "1000000000000002")  // held by the asker only
	seedNodeWithSerial(t, repo, mine, "1000000000000003")  // held by both …
	seedNodeWithSerial(t, repo, other, "1000000000000003") // … so two rows collapse into one
	seedNodeWithSerial(t, repo, other, "1000000000000004") // duplicated within the other account
	seedNodeWithSerial(t, repo, other, "1000000000000004")
	seedNodeWithSerial(t, repo, other, "ABCDEF0123456789") // stored upper-case; asked lower-case

	scopes, err := repo.SerialBindingScopes(ctx, mine, []string{
		"1000000000000001",
		"1000000000000002",
		"1000000000000003",
		"1000000000000004",
		"abcdef0123456789",
		"1000000000000009", // bound nowhere: must be ABSENT, not present-and-false
	})
	if err != nil {
		t.Fatalf("SerialBindingScopes: %v", err)
	}

	want := map[string][2]bool{ // {InAccount, InAnotherAccount}
		"1000000000000001": {false, true},
		"1000000000000002": {true, false},
		"1000000000000003": {true, true},
		"1000000000000004": {false, true},
		"abcdef0123456789": {false, true},
	}
	if len(scopes) != len(want) {
		t.Errorf("got %d scopes, want %d: %+v", len(scopes), len(want), scopes)
	}
	for serial, w := range want {
		got, ok := scopes[serial]
		if !ok {
			t.Errorf("%s: missing from the result", serial)
			continue
		}
		if got.InAccount != w[0] || got.InAnotherAccount != w[1] {
			t.Errorf("%s: got in_account=%v in_another=%v, want %v/%v", serial, got.InAccount, got.InAnotherAccount, w[0], w[1])
		}
	}
	if _, ok := scopes["1000000000000009"]; ok {
		t.Errorf("a serial bound nowhere must not appear in the result")
	}
}

// An empty ask returns an empty map and never reaches the database — GORM would
// render the IN as `IN (NULL)`, a full scan of nodes for nothing.
func TestSerialBindingScopesEmptyInputQueriesNothing(t *testing.T) {
	repo := serialScopesRepo(t)
	// Break the table under the repository: if the call touched it, it would
	// error rather than answer.
	if err := repo.db.Exec(`DROP TABLE nodes CASCADE`).Error; err != nil {
		t.Fatalf("dropping nodes: %v", err)
	}
	scopes, err := repo.SerialBindingScopes(context.Background(), uuid.New(), nil)
	if err != nil {
		t.Fatalf("empty input reached the database: %v", err)
	}
	if len(scopes) != 0 {
		t.Errorf("got %d scopes for an empty input", len(scopes))
	}
}
