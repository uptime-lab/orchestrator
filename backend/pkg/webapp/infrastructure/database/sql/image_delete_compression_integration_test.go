package sql

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// Delete must refuse while a compression job holds the row: the job's final
// store would otherwise recreate the object AFTER the row and its files were
// deleted — an orphan invisible to the byte quota and to an account purge, which
// derives object keys from rows. The guard lives in the DELETE's
// WHERE clause so it serializes against ClaimCompression instead of racing it.
func TestDeleteRefusedWhileCompressionInProgress(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	img := image.NewImage(accountID, "raw.img", "", "", "")
	if err := repo.Create(ctx, img); err != nil {
		t.Fatalf("seeding image: %v", err)
	}

	won, err := repo.ClaimCompression(ctx, accountID, img.ID)
	if err != nil || !won {
		t.Fatalf("claiming compression: won=%v err=%v", won, err)
	}

	if err := repo.Delete(ctx, accountID, img.ID); !errors.Is(err, app_errors.ErrImageCompressionInProgress) {
		t.Fatalf("delete during compression: got %v, want ErrImageCompressionInProgress", err)
	}
	if _, err := repo.GetByID(ctx, accountID, img.ID); err != nil {
		t.Fatalf("row must survive a refused delete: %v", err)
	}

	// Once the job settles the claim, the same delete goes through.
	if err := repo.SetCompressionState(ctx, accountID, img.ID, image.CompressionFailed, 0); err != nil {
		t.Fatalf("settling compression state: %v", err)
	}
	if err := repo.Delete(ctx, accountID, img.ID); err != nil {
		t.Fatalf("delete after compression settled: %v", err)
	}

	// A genuinely missing image still answers not-found, not conflict.
	if err := repo.Delete(ctx, accountID, img.ID); !errors.Is(err, app_errors.ErrImageNotFound) {
		t.Fatalf("delete of a deleted image: got %v, want ErrImageNotFound", err)
	}
}

// An abandoned capture must STAY deletable — blocking it would let one stuck
// capture brick image cleanup, the deliberate design CountByAccountID protects.
func TestDeleteAllowedWhileCaptureInProgress(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	img := image.NewImage(accountID, "capture.img", "", "", "")
	if err := repo.Create(ctx, img); err != nil {
		t.Fatalf("seeding image: %v", err)
	}
	if err := repo.SetCompressionState(ctx, accountID, img.ID, image.CaptureInProgress, 0); err != nil {
		t.Fatalf("marking capture in progress: %v", err)
	}

	if err := repo.Delete(ctx, accountID, img.ID); err != nil {
		t.Fatalf("delete of an in-progress capture must succeed: %v", err)
	}
}
