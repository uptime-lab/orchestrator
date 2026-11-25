package sql

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// ClaimCaptureUpload must admit EXACTLY ONE of several concurrent callers — the
// property that makes a replayed capture PUT lose while the legitimate stream is
// in flight. The capability is a stateless HMAC valid for hours, so the claim,
// not the signature, is the single-use guarantee; a check-then-act on
// compression_status let every concurrent caller through.
func TestClaimCaptureUploadAdmitsExactlyOneConcurrentCaller(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	img := image.NewCaptureImage(uuid.New(), accountID, "golden", false, uuid.New(), "node-1")
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
			won, err := repo.ClaimCaptureUpload(ctx, img.ID)
			if err != nil {
				t.Errorf("ClaimCaptureUpload: %v", err)
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
		t.Fatalf("wins = %d, want exactly 1", wins)
	}

	got, err := repo.GetByIDGlobal(ctx, img.ID)
	if err != nil {
		t.Fatalf("reloading image: %v", err)
	}
	if got.CompressionStatus != image.CaptureUploadInProgress {
		t.Fatalf("status = %q, want %q", got.CompressionStatus, image.CaptureUploadInProgress)
	}

	// A finalized record (empty status) must be unclaimable: that is what makes
	// a leaked capability URL harmless after the capture completes.
	finalized := *got
	finalized.CompressionStatus = ""
	won, err := repo.UpdateIfCaptureUploading(ctx, &finalized)
	if err != nil || !won {
		t.Fatalf("finalizing under our own claim: won=%v err=%v", won, err)
	}
	won, err = repo.ClaimCaptureUpload(ctx, img.ID)
	if err != nil {
		t.Fatalf("ClaimCaptureUpload on a finalized image: %v", err)
	}
	if won {
		t.Fatal("a finalized image was claimable for a second capture stream")
	}

	// CaptureFailed stays claimable — that is the retry path.
	failed := finalized
	failed.CompressionStatus = image.CaptureFailed
	if err := repo.Update(ctx, &failed); err != nil {
		t.Fatalf("seeding a failed capture: %v", err)
	}
	won, err = repo.ClaimCaptureUpload(ctx, img.ID)
	if err != nil || !won {
		t.Fatalf("a failed capture must be claimable for retry: won=%v err=%v", won, err)
	}
}
