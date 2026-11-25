package sql

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

func imageRepoForClaimTest(t *testing.T) *ImageRepository {
	t.Helper()
	dsn := os.Getenv("BACKEND_TEST_DSN")
	if dsn == "" {
		t.Skip("BACKEND_TEST_DSN not set — skipping DB integration test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	if err := db.Exec(`DROP TABLE IF EXISTS images CASCADE`).Error; err != nil {
		t.Fatalf("dropping: %v", err)
	}
	if err := db.AutoMigrate(&ImageModel{}); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return NewImageRepository(db)
}

// ClaimCompression must let EXACTLY ONE of several concurrent callers in — that is
// the property that stops two compress jobs from racing and deleting each other's
// published object. A check-then-act would let several through.
func TestClaimCompressionAdmitsExactlyOneConcurrentCaller(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	img := image.NewImage(accountID, "raw.img", "", "", "")
	if err := repo.Create(ctx, img); err != nil {
		t.Fatalf("seeding image: %v", err)
	}

	const racers = 16
	var wins int32
	var wg sync.WaitGroup
	wg.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start
			won, err := repo.ClaimCompression(ctx, accountID, img.ID)
			if err != nil {
				t.Errorf("claim error: %v", err)
				return
			}
			if won {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Errorf("%d callers won the compression claim, want exactly 1", wins)
	}
}
