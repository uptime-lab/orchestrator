package sql

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The jsonb coverage is SQL — a ::text cast, a COALESCE and keyset batching — and
// the whole point is that an answer of "the old key can be
// dropped" is acted on irreversibly. Unit tests cover the classification; this
// covers the query.
//
//	BACKEND_TEST_DSN="host=localhost port=5434 user=postgres password=postgres dbname=backend_test sslmode=disable" go test ./pkg/webapp/infrastructure/database/sql/
func coverageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS node_commands CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	if err := db.AutoMigrate(&NodeCommandModel{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return db
}

func seedCommandPayload(t *testing.T, db *gorm.DB, payload string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := db.Exec(`INSERT INTO node_commands
		 (id, node_id, account_id, slug, status, progress, queueable, attempts, payload, created_at, updated_at)
		 VALUES (?, ?, ?, 'reboot', 'success', 100, false, 0, ?::jsonb, now(), now())`,
		id, uuid.New(), uuid.New(), payload).Error
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return id
}

func tokenBlob(t *testing.T, enc interface {
	Encrypt(string) (string, error)
}, plaintext string) string {
	t.Helper()
	ct, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	blob, err := json.Marshal(map[string]string{"password": commandCipherPrefix + ct, "path": "/plain"})
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	return string(blob)
}

func coverageFor(report []KeyCoverage, target string) KeyCoverage {
	for _, c := range report {
		if c.Target == target {
			return c
		}
	}
	return KeyCoverage{Target: "MISSING:" + target, Unreadable: -1}
}

// The property itself: a command payload still on the old key must be counted, or
// the report says the old key can be dropped while queued commands depend on it —
// and decryptCommandPayload DELETES fields it cannot read, so those commands are
// dispatched with parameters silently missing.
func TestCommandPayloadsOnTheOldKeyAreCounted(t *testing.T) {
	db := coverageTestDB(t)
	rotated := crypto.NewEncryptorWithFallbacks("new-primary", []string{"old"})

	seedCommandPayload(t, db, tokenBlob(t, rotated, "already-current"))
	seedCommandPayload(t, db, tokenBlob(t, crypto.NewEncryptor("old"), "still-on-the-old-key"))

	report := VerifyEncryptionKeyCoverage(db, rotated, CoverageFull)
	cov := coverageFor(report, "node_commands.payload/result/details")

	if cov.OnPrimary != 1 || cov.OnOldKey != 1 || cov.Unreadable != 0 {
		t.Fatalf("counted primary=%d old_key=%d unreadable=%d, want 1/1/0", cov.OnPrimary, cov.OnOldKey, cov.Unreadable)
	}
	if KeyCoverageIsClean(report) {
		t.Fatal("reported the old key as droppable while a command payload still needs it")
	}
}

// A payload nothing can read is the restore-with-the-wrong-key signal, and it
// must be distinguishable from "still on the old key".
func TestAnUndecryptablePayloadCountsAsUnreadable(t *testing.T) {
	db := coverageTestDB(t)
	rotated := crypto.NewEncryptorWithFallbacks("new-primary", []string{"old"})

	seedCommandPayload(t, db, tokenBlob(t, crypto.NewEncryptor("a-key-nobody-configured"), "lost"))

	report := VerifyEncryptionKeyCoverage(db, rotated, CoverageFull)
	cov := coverageFor(report, "node_commands.payload/result/details")

	if cov.Unreadable != 1 || cov.OnOldKey != 0 {
		t.Fatalf("counted old_key=%d unreadable=%d, want 0/1", cov.OnOldKey, cov.Unreadable)
	}
	if !KeyCoverageHasUnreadable(report) {
		t.Fatal("the wrong-key alarm did not fire on a payload no configured key can read")
	}
}

// Payloads with no ciphertext at all are the overwhelming majority of the table.
// Counting one of them would make every rotation look incomplete forever.
func TestPlainPayloadsAreNotCounted(t *testing.T) {
	db := coverageTestDB(t)
	enc := crypto.NewEncryptor("primary")

	seedCommandPayload(t, db, `{"target_device":"/dev/nvme0n1"}`)
	seedCommandPayload(t, db, `{}`)

	cov := coverageFor(VerifyEncryptionKeyCoverage(db, enc, CoverageFull), "node_commands.payload/result/details")
	if cov.OnPrimary != 0 || cov.OnOldKey != 0 || cov.Unreadable != 0 {
		t.Fatalf("counted primary=%d old_key=%d unreadable=%d for payloads holding no ciphertext",
			cov.OnPrimary, cov.OnOldKey, cov.Unreadable)
	}
}

// The batching is keyset-based, so it has to terminate and not re-read a page.
// One row past the batch size is the case that catches an off-by-one.
func TestTheScanCoversEveryRowAcrossBatches(t *testing.T) {
	db := coverageTestDB(t)
	enc := crypto.NewEncryptor("primary")

	rows := backfillBatchSize + 1
	for i := 0; i < rows; i++ {
		seedCommandPayload(t, db, tokenBlob(t, enc, "value"))
	}

	cov := coverageFor(VerifyEncryptionKeyCoverage(db, enc, CoverageFull), "node_commands.payload/result/details")
	if cov.OnPrimary != rows {
		t.Fatalf("counted %d values across %d rows; the batch loop lost or repeated a page", cov.OnPrimary, rows)
	}
	if cov.Truncated {
		t.Fatal("a full scan reported itself as sampled")
	}
}

// The sampled scope exists so the boot-time canary does not walk a table with no
// retention. It must stop, and it must say that it stopped.
func TestTheSampledScopeStopsAndDisclosesIt(t *testing.T) {
	db := coverageTestDB(t)
	enc := crypto.NewEncryptor("primary")

	for i := 0; i < coverageSampleSize+backfillBatchSize; i++ {
		seedCommandPayload(t, db, tokenBlob(t, enc, "value"))
	}

	cov := coverageFor(VerifyEncryptionKeyCoverage(db, enc, CoverageSample), "node_commands.payload/result/details")
	if !cov.Truncated {
		t.Fatal("the sampled scan did not disclose that it stopped early")
	}
	if cov.OnPrimary == 0 {
		t.Fatal("the sampled scan examined nothing")
	}
	if cov.OnPrimary > coverageSampleSize+backfillBatchSize {
		t.Fatalf("examined %d values; the sample is not bounded", cov.OnPrimary)
	}
}

// The third column. Sealing node_commands.details widened this target to three
// columns while tokenColumnCoverage's row type held two; its own guard then
// returned the -1 sentinel, which broke the coverage report for the LARGEST
// rewrap target until CI caught it. A token that lives only
// in details must be counted like any payload token.
func TestDetailsTokensAreCounted(t *testing.T) {
	db := coverageTestDB(t)
	enc := crypto.NewEncryptor("primary")

	sealed, err := enc.Encrypt("curl: (22) https://user:hunter2@repo/ 401")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	details := commandCipherPrefix + sealed
	if err := db.Exec(`INSERT INTO node_commands
		 (id, node_id, account_id, slug, status, progress, queueable, attempts, details, created_at, updated_at)
		 VALUES (?, ?, ?, 'reboot', 'success', 100, false, 0, ?, now(), now())`,
		uuid.New(), uuid.New(), uuid.New(), details).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	cov := coverageFor(VerifyEncryptionKeyCoverage(db, enc, CoverageFull), "node_commands.payload/result/details")
	if cov.Unreadable < 0 {
		t.Fatalf("coverage returned the guard sentinel (%+v); the details column never reached the scan", cov)
	}
	if cov.OnPrimary < 1 {
		t.Fatalf("counted %d primary tokens, want the details token counted — a rotation report blind to details calls the old key droppable while error text still needs it", cov.OnPrimary)
	}
}
