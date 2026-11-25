package sql

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
)

func secretRepoForTest(t *testing.T) *SecretRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS secrets CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	if err := db.AutoMigrate(&SecretModel{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return NewSecretRepository(db, passthroughEncryptor{})
}

// secret_upsert advertises kind as editable, but Update's column whitelist left
// "kind" out — so an opaque->env change reported success while persisting nothing,
// and since only env-kind secrets are delivered to a device, the variable never
// arrived. This drives the repo directly: the kind change must land.
func TestSecretUpdatePersistsKind(t *testing.T) {
	repo := secretRepoForTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	sec, err := secret.NewSecret(accountID, "api-token", secret.KindOpaque, "TOKEN", "v1", true)
	if err != nil {
		t.Fatalf("new secret: %v", err)
	}
	if err := repo.Create(ctx, sec); err != nil {
		t.Fatalf("create: %v", err)
	}

	sec.Kind = secret.KindEnv
	if err := repo.Update(ctx, sec); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.GetByID(ctx, accountID, sec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != secret.KindEnv {
		t.Errorf("kind = %q after update, want %q — the kind change did not persist", got.Kind, secret.KindEnv)
	}
}
