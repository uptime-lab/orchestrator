package sql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// A page size is expressed in SQL, so it can only be asserted against a real
// database — a fake would page however the fake pages. What is at stake is a
// boot: node_files holds 512 KiB-capped file versions, the coverage canary reads
// the column on every start and the rewrap reads it during a key rotation, and
// either one holding a 500-row page is hundreds of megabytes inside a
// memory-limited container.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/

// statementRecorder captures the SQL gorm actually issued, with its bind
// variables already substituted.
type statementRecorder struct {
	logger.Interface
	mu         sync.Mutex
	statements []string
}

func (r *statementRecorder) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = append(r.statements, sql)
}

func (r *statementRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statements = nil
}

// selectsAgainst returns the recorded reads of one table.
func (r *statementRecorder) selectsAgainst(table string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.statements {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(s)), "SELECT") && strings.Contains(s, table) {
			out = append(out, s)
		}
	}
	return out
}

// nodeFileSweepTestDB gives a node_files table holding rows of encrypted content
// and a recorder of every statement issued against it.
func nodeFileSweepTestDB(t *testing.T, rows int, enc interface{ Encrypt(string) (string, error) }) (*gorm.DB, *statementRecorder) {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	rec := &statementRecorder{Interface: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: rec})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	stmts := []string{
		`DROP TABLE IF EXISTS node_files CASCADE`,
		`CREATE TABLE node_files (
		   id uuid PRIMARY KEY, account_id uuid, node_id uuid NOT NULL,
		   path text NOT NULL, content text NOT NULL DEFAULT '', hash text NOT NULL DEFAULT '',
		   content_enc int NOT NULL DEFAULT 0, version int NOT NULL,
		   status text NOT NULL DEFAULT 'applied', error_message text,
		   created_at timestamptz NOT NULL DEFAULT now(),
		   updated_at timestamptz NOT NULL DEFAULT now())`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	nodeID := uuid.New()
	for i := 0; i < rows; i++ {
		content, err := enc.Encrypt(fmt.Sprintf("tracked file %d", i))
		if err != nil {
			t.Fatalf("encrypting: %v", err)
		}
		err = db.Exec(`INSERT INTO node_files (id, node_id, path, content, hash, content_enc, version)
			 VALUES (?, ?, ?, ?, '', 1, ?)`,
			uuid.New(), nodeID, fmt.Sprintf("/etc/file-%d", i), content, i+1).Error
		if err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	rec.reset()
	return db, rec
}

// assertPagedByTheLargeRowBatch is the whole point: every read of the column is
// bounded, and it is bounded by the size that fits file contents.
func assertPagedByTheLargeRowBatch(t *testing.T, rec *statementRecorder) {
	t.Helper()
	reads := rec.selectsAgainst("node_files")
	if len(reads) < 2 {
		t.Fatalf("the column was read in %d statement(s); a full page plus one row must take more than one page", len(reads))
	}
	want := fmt.Sprintf("LIMIT %d", contentBackfillBatchSize)
	for _, sql := range reads {
		if !strings.Contains(sql, want) {
			t.Fatalf("a read of node_files.content is not bounded by the large-row batch (%s): %s", want, sql)
		}
	}
}

func TestTrackedFileCoverageReadsTheColumnInLargeRowPages(t *testing.T) {
	enc := crypto.NewEncryptor("primary-key-for-the-coverage-scan")
	seeded := contentBackfillBatchSize + 1
	db, rec := nodeFileSweepTestDB(t, seeded, enc)

	cov := coverageFor(VerifyEncryptionKeyCoverage(db, enc, CoverageFull), "node_files.content")
	if cov.OnPrimary != seeded {
		t.Fatalf("classified %d values across %d rows; the batch loop lost or repeated a page", cov.OnPrimary, seeded)
	}
	if cov.Truncated {
		t.Fatal("a full scan reported itself as sampled")
	}
	assertPagedByTheLargeRowBatch(t, rec)
}

func TestTrackedFileRewrapReadsTheColumnInLargeRowPages(t *testing.T) {
	enc := crypto.NewEncryptor("primary-key-for-the-rewrap")
	db, rec := nodeFileSweepTestDB(t, contentBackfillBatchSize+1, enc)

	var target encryptedTarget
	for _, candidate := range encryptedTargets() {
		if candidate.Label == "node_files.content" {
			target = candidate
		}
	}
	if target.Rewrap == nil {
		t.Fatal("node_files.content is missing from the encrypted-target inventory")
	}
	if err := target.Rewrap(db, enc); err != nil {
		t.Fatalf("rewrapping: %v", err)
	}
	assertPagedByTheLargeRowBatch(t, rec)
}
