package image

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// A capture whose command ended without streaming moves its record out of
// "capturing" — and only that state: a stream in flight settles its own
// record, a finished one is not this caller's to rewrite, and another
// account's record is out of reach.
func TestFailAwaitingCaptureMovesOnlyARecordStillAwaitingItsStream(t *testing.T) {
	for _, tc := range []struct {
		from string
		want string
	}{
		{image.CaptureInProgress, image.CaptureFailed},
		{image.CaptureUploadInProgress, image.CaptureUploadInProgress},
		{image.CaptureFailed, image.CaptureFailed},
		{"", ""},
	} {
		t.Run("from "+tc.from, func(t *testing.T) {
			repo := newFakeImageRepo()
			svc := NewService(repo, newFakeStorage(), &config.Config{})
			accountID := uuid.New()
			img := image.NewCaptureImage(uuid.New(), accountID, "golden", false, uuid.New(), "node")
			img.CompressionStatus = tc.from
			repo.images[img.ID] = img

			if err := svc.FailAwaitingCapture(context.Background(), accountID, img.ID); err != nil {
				t.Fatalf("FailAwaitingCapture: %v", err)
			}
			if got := repo.images[img.ID].CompressionStatus; got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFailAwaitingCaptureIsAccountScoped(t *testing.T) {
	repo := newFakeImageRepo()
	svc := NewService(repo, newFakeStorage(), &config.Config{})
	img := image.NewCaptureImage(uuid.New(), uuid.New(), "golden", false, uuid.New(), "node")
	repo.images[img.ID] = img

	if err := svc.FailAwaitingCapture(context.Background(), uuid.New(), img.ID); err != nil {
		t.Fatalf("FailAwaitingCapture: %v", err)
	}
	if got := repo.images[img.ID].CompressionStatus; got != image.CaptureInProgress {
		t.Errorf("another account's call moved the record to %q", got)
	}
}
