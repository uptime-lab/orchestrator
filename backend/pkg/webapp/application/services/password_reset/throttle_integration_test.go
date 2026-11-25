package password_reset

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/database/sql"
)

func passwordResetRepoForTest(t *testing.T) *sql.PasswordResetRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS password_resets CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	repo := sql.NewPasswordResetRepository(db)
	if err := repo.Setup(); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return repo
}

// The throttle counts rows on the same table the send path revokes, so only
// real SQL can say whether a send is still visible to the next count. Driven
// through the actual repository for that reason: the stub above answers a
// hardcoded count and would pass either way.
func TestRepeatedResetRequestsReachTheThrottle(t *testing.T) {
	repo := passwordResetRepoForTest(t)
	sender := &stubSender{}
	usr := user.NewUser("owner@example.com", "Owner", "existing-hash")
	svc := NewService(repo, &stubUserRepo{user: usr}, sender, "https://app.example.com", slog.Default())

	const attempts = maxSendsPerWindow + 3
	for i := 0; i < attempts; i++ {
		if err := svc.Request(context.Background(), usr.Email); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}

	if len(sender.sent) != maxSendsPerWindow {
		t.Fatalf("%d requests produced %d emails, want %d — the per-mailbox throttle did not bind",
			attempts, len(sender.sent), maxSendsPerWindow)
	}
}
