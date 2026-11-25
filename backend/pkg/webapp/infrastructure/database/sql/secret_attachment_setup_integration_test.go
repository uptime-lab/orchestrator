package sql

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func attachmentRepoForSetupTest(t *testing.T) (*SecretAttachmentRepository, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS secret_attachments CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	repo := NewSecretAttachmentRepository(db)
	if err := repo.Setup(); err != nil {
		t.Fatalf("first Setup: %v", err)
	}
	return repo, db
}

// Setup runs on EVERY backend start, and its dedupe used to partition by
// (account_id, secret_id, ou_id, node_id) — without `tag`. A tag attachment
// carries a NULL secret_id, Postgres groups NULLs into one window partition, so
// every tag attached to one target collapsed into a single row on the next
// restart and the secrets the others delivered silently stopped reaching the
// devices.
//
// This is the test the original could not have had: it needs a real Postgres,
// because the whole defect lives in how the database groups NULLs.
func TestSetupKeepsEveryTagAttachedToOneTarget(t *testing.T) {
	repo, db := attachmentRepoForSetupTest(t)
	accountID := uuid.New()
	ouID := uuid.New()

	for _, tag := range []string{"infra", "ci", "prod"} {
		if err := db.Exec(
			`INSERT INTO secret_attachments (id, account_id, tag, ou_id) VALUES (?, ?, ?, ?)`,
			uuid.New(), accountID, tag, ouID).Error; err != nil {
			t.Fatalf("seeding %q: %v", tag, err)
		}
	}

	// A restart.
	if err := repo.Setup(); err != nil {
		t.Fatalf("second Setup: %v", err)
	}

	var count int64
	if err := db.Raw(`SELECT count(*) FROM secret_attachments WHERE ou_id = ?`, ouID).
		Scan(&count).Error; err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 3 {
		t.Errorf("%d of 3 tag attachments survived a restart", count)
	}
}

// The dedupe still has to do its job: rows the partial unique indexes would
// reject must go, or index creation fails and the backend does not boot.
func TestSetupStillRemovesRealDuplicates(t *testing.T) {
	repo, db := attachmentRepoForSetupTest(t)
	accountID := uuid.New()
	ouID := uuid.New()
	secretID := uuid.New()

	// Drop the unique indexes first: they are what a legacy install did NOT have
	// when the duplicates were created, and with them in place the seed cannot
	// even be written. Setup() recreates them at the end, which is the step that
	// fails if the dedupe stops working.
	for _, idx := range []string{
		"uq_secret_attachments_secret_ou", "uq_secret_attachments_secret_node",
		"uq_secret_attachments_tag_ou", "uq_secret_attachments_tag_node",
	} {
		if err := db.Exec(`DROP INDEX IF EXISTS ` + idx).Error; err != nil {
			t.Fatalf("dropping %s: %v", idx, err)
		}
	}

	// Two rows for the same secret on the same OU, and two for the same tag on
	// the same OU — one of each must survive.
	for i := 0; i < 2; i++ {
		if err := db.Exec(
			`INSERT INTO secret_attachments (id, account_id, secret_id, tag, ou_id) VALUES (?, ?, ?, '', ?)`,
			uuid.New(), accountID, secretID, ouID).Error; err != nil {
			t.Fatalf("seeding secret duplicate: %v", err)
		}
		if err := db.Exec(
			`INSERT INTO secret_attachments (id, account_id, tag, ou_id) VALUES (?, ?, 'infra', ?)`,
			uuid.New(), accountID, ouID).Error; err != nil {
			t.Fatalf("seeding tag duplicate: %v", err)
		}
	}

	if err := repo.Setup(); err != nil {
		t.Fatalf("second Setup: %v", err)
	}

	var secretRows, tagRows int64
	if err := db.Raw(`SELECT count(*) FROM secret_attachments WHERE secret_id = ?`, secretID).
		Scan(&secretRows).Error; err != nil {
		t.Fatalf("counting secret rows: %v", err)
	}
	if err := db.Raw(`SELECT count(*) FROM secret_attachments WHERE tag = 'infra' AND ou_id = ?`, ouID).
		Scan(&tagRows).Error; err != nil {
		t.Fatalf("counting tag rows: %v", err)
	}
	if secretRows != 1 {
		t.Errorf("duplicate secret attachments left %d rows, want 1", secretRows)
	}
	if tagRows != 1 {
		t.Errorf("duplicate tag attachments left %d rows, want 1", tagRows)
	}
}
