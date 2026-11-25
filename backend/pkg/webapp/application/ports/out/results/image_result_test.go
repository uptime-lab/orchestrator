package results

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

func TestNewImageResultFallsBackToCreatedAtForLegacyUploads(t *testing.T) {
	created := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	img := &image.Image{
		ID:        uuid.New(),
		AccountID: uuid.New(),
		Title:     "legacy",
		CreatedAt: created,
		UpdatedAt: created,
		FileStorageInfo: &image.FileStorageInfo{
			StorageType: "local",
			FilePath:    "acc/img.img",
			// UploadedAt left zero, as stored before the field existed.
		},
	}

	result := NewImageResult(img)
	if !result.FileStorageInfo.UploadedAt.Equal(created) {
		t.Errorf("uploaded_at = %v, want fallback to created_at %v", result.FileStorageInfo.UploadedAt, created)
	}
	// The domain object must not be mutated by the presentation fallback.
	if !img.FileStorageInfo.UploadedAt.IsZero() {
		t.Error("domain FileStorageInfo must stay untouched")
	}
}

func TestNewImageResultKeepsRealUploadedAt(t *testing.T) {
	uploaded := time.Date(2026, 7, 11, 7, 0, 0, 0, time.UTC)
	img := &image.Image{
		ID:        uuid.New(),
		AccountID: uuid.New(),
		CreatedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		FileStorageInfo: &image.FileStorageInfo{
			StorageType: "local",
			FilePath:    "acc/img.zst",
			UploadedAt:  uploaded,
		},
	}
	result := NewImageResult(img)
	if !result.FileStorageInfo.UploadedAt.Equal(uploaded) {
		t.Errorf("uploaded_at = %v, want %v", result.FileStorageInfo.UploadedAt, uploaded)
	}
}
