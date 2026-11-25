package sql

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/crypto"
)

// secretRepoWithKey opens a repository over the shared scratch database with a
// specific encryption key, so one test can write rows under two different keys —
// which is what a rotation that dropped SECRETS_ENCRYPTION_KEY_OLD one boot too
// early leaves behind.
func secretRepoWithKey(t *testing.T, key string, migrate bool) *SecretRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if migrate {
		if err := db.Exec(`DROP TABLE IF EXISTS secrets CASCADE`).Error; err != nil {
			t.Fatalf("dropping: %v", err)
		}
		if err := db.AutoMigrate(&SecretModel{}); err != nil {
			t.Fatalf("migrating: %v", err)
		}
	}
	return NewSecretRepository(db, crypto.NewEncryptor(key))
}

// One row the current key cannot decrypt used to abort the whole read, so every
// page of the Secrets list containing it answered 500 and the edit form behind
// GetByID failed too — leaving psql as the only way to find or repair the row.
// The rest of the account's secrets must still come back, and the bad one must
// be reported rather than omitted.
func TestSecretListReportsOneUndecryptableRowAndKeepsTheRest(t *testing.T) {
	const currentKey = "current-key-0123456789abcdefghijklmn"
	const droppedKey = "dropped-key-0123456789abcdefghijklmn"

	current := secretRepoWithKey(t, currentKey, true)
	dropped := secretRepoWithKey(t, droppedKey, false)
	ctx := context.Background()
	accountID := uuid.New()

	readable, err := secret.NewSecret(accountID, "a-readable", secret.KindEnv, "READABLE", "still-here", false)
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	if err := current.Create(ctx, readable); err != nil {
		t.Fatalf("create readable: %v", err)
	}
	// Written under the key that has since been dropped.
	orphan, err := secret.NewSecret(accountID, "b-orphan", secret.KindOpaque, "", "unrecoverable", true)
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	if err := dropped.Create(ctx, orphan); err != nil {
		t.Fatalf("create orphan: %v", err)
	}
	trailing, err := secret.NewSecret(accountID, "c-readable", secret.KindEnv, "ALSO", "fine", false)
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	if err := current.Create(ctx, trailing); err != nil {
		t.Fatalf("create trailing: %v", err)
	}

	list, total, err := current.List(ctx, accountID, repository.SecretListFilter{Sort: "name"})
	if err != nil {
		t.Fatalf("List returned an error for a page holding one undecryptable row: %v", err)
	}
	if total != 3 || len(list) != 3 {
		t.Fatalf("List returned %d of %d rows, want 3 of 3", len(list), total)
	}
	byName := make(map[string]secret.Secret, len(list))
	for i := range list {
		byName[list[i].Name] = list[i]
	}
	if got := byName["a-readable"]; got.Value != "still-here" || got.ValueUnreadable {
		t.Errorf("readable secret came back value=%q unreadable=%t", got.Value, got.ValueUnreadable)
	}
	bad := byName["b-orphan"]
	if !bad.ValueUnreadable {
		t.Error("the orphaned row is not flagged unreadable, so the operator cannot tell it apart from an empty one")
	}
	if bad.Value != "" {
		t.Errorf("unreadable secret leaked a value %q", bad.Value)
	}
	if !bad.HasValue() {
		t.Error("HasValue() is false for a stored-but-unrecoverable value; it would read as 'never set'")
	}

	one, err := current.GetByID(ctx, accountID, orphan.ID)
	if err != nil {
		t.Fatalf("GetByID (the edit form) failed for the undecryptable row: %v", err)
	}
	if !one.ValueUnreadable || one.Value != "" {
		t.Errorf("GetByID returned value=%q unreadable=%t", one.Value, one.ValueUnreadable)
	}
}
