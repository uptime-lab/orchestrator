package sql

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// FailAwaitingCapture moves a capture record only out of `capturing`, and only
// within its own account: a stream in flight settles its own record.
func TestFailAwaitingCaptureMovesOnlyAnAwaitingRecordOfItsAccount(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	seed := func(status string) *image.Image {
		img := image.NewCaptureImage(uuid.New(), accountID, "golden", false, uuid.New(), "node")
		img.CompressionStatus = status
		if err := repo.Create(ctx, img); err != nil {
			t.Fatalf("seeding: %v", err)
		}
		return img
	}
	awaiting := seed(image.CaptureInProgress)
	streaming := seed(image.CaptureUploadInProgress)

	if won, err := repo.FailAwaitingCapture(ctx, uuid.New(), awaiting.ID); err != nil || won {
		t.Fatalf("another account: won=%v err=%v, want false/nil", won, err)
	}
	if won, err := repo.FailAwaitingCapture(ctx, accountID, streaming.ID); err != nil || won {
		t.Fatalf("a stream in flight: won=%v err=%v, want false/nil", won, err)
	}
	if won, err := repo.FailAwaitingCapture(ctx, accountID, awaiting.ID); err != nil || !won {
		t.Fatalf("an awaiting record: won=%v err=%v, want true/nil", won, err)
	}
	for id, want := range map[uuid.UUID]string{awaiting.ID: image.CaptureFailed, streaming.ID: image.CaptureUploadInProgress} {
		got, err := repo.GetByID(ctx, accountID, id)
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got.CompressionStatus != want {
			t.Errorf("status = %q, want %q", got.CompressionStatus, want)
		}
	}
}
