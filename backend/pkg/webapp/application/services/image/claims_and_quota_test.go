package image

import (
	"context"
	"errors"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// A metadata edit must not be able to say anything about a claim.
//
// The PUT went through Update — `Select("*").Updates`, the only writer of
// `images` that is not claim-conditional. A title edit whose read landed just
// before ClaimUpload or ClaimCompression wrote compression_status="" back OVER
// the claim, after which a concurrent compress legitimately won and both jobs
// wrote the same deterministic object key: the exact disaster the exclusive
// claim exists to prevent. The UI serialises update-then-upload, so the UI never
// triggered it; a second tab, a second operator or any API client does.
func TestMetadataUpdateDoesNotClobberAConcurrentClaim(t *testing.T) {
	repo := newFakeImageRepo()
	svc := NewService(repo, newFakeStorage(), &config.Config{})
	ctx := context.Background()

	accountID, imageID := uuid.New(), uuid.New()
	if err := repo.Create(ctx, &image.Image{ID: imageID, AccountID: accountID, Title: "before"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A compression claims the record in the window between the edit's read and
	// its write. Driving it from the read hook is the only way to reach that
	// window: the service reads the record itself, so a claim taken before the
	// call is simply part of what the edit reads back.
	var once bool
	repo.afterGet = func() {
		if once {
			return
		}
		once = true
		if claimed, err := repo.ClaimCompression(ctx, accountID, imageID); err != nil || !claimed {
			t.Errorf("ClaimCompression = %v, %v; want true, nil", claimed, err)
		}
	}

	if _, err := svc.Update(ctx, image_update.ImageUpdateCommand{
		ID:          imageID,
		Title:       "after",
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	repo.afterGet = nil

	stored, err := repo.GetByID(ctx, accountID, imageID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.CompressionStatus != image.CompressionInProgress {
		t.Fatalf("compression_status = %q after a title edit, want %q — the edit released a claim it never took, and two jobs can now write the same object key",
			stored.CompressionStatus, image.CompressionInProgress)
	}
	if stored.Title != "after" {
		t.Errorf("title = %q, want %q: the edit must still land", stored.Title, "after")
	}
}

// The metadata edit must also not write a stale file pointer over a finished
// compression: with IMAGE_COMPRESS_REPLACE_ORIGINAL=true that leaves the
// record pointing at a deleted object.
func TestMetadataUpdateDoesNotRevertTheStoredFile(t *testing.T) {
	repo := newFakeImageRepo()
	svc := NewService(repo, newFakeStorage(), &config.Config{})
	ctx := context.Background()

	accountID, imageID := uuid.New(), uuid.New()
	seed := &image.Image{ID: imageID, AccountID: accountID, Title: "before"}
	seed.SetFileStorageInfo("local", "acct/old.img", "old.img", 100)
	if err := repo.Create(ctx, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A compression finishes in the window between the edit's read and its write.
	var once bool
	repo.afterGet = func() {
		if once {
			return
		}
		once = true
		finished := *seed
		finished.SetFileStorageInfo("local", "acct/new.img.xz", "new.img.xz", 40)
		finished.Checksum = "the-new-one"
		if err := repo.Update(ctx, &finished); err != nil {
			t.Errorf("compression write: %v", err)
		}
	}
	defer func() { repo.afterGet = nil }()

	if _, err := svc.Update(ctx, image_update.ImageUpdateCommand{
		ID:          imageID,
		Title:       "after",
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	stored, _ := repo.GetByID(ctx, accountID, imageID)
	if stored.FileStorageInfo == nil || stored.FileStorageInfo.FilePath != "acct/new.img.xz" {
		t.Fatalf("file path = %v, want the compressed one; the record now names an object that was deleted", stored.FileStorageInfo)
	}
	if stored.Checksum != "the-new-one" {
		t.Errorf("checksum = %q, want the compression's", stored.Checksum)
	}
}

// The byte ceiling has to be re-checked once the real size is known.
//
// The handler's pre-check is a plain SUM, and an upload's bytes enter that sum
// only when the record is written at the very END — so a second upload starting
// while the first is minutes into streaming reads a budget that does not account
// for it. Three 5 GiB files then land against a 10 GiB account cap, and the same
// across accounts for IMAGE_MAX_BYTES_INSTANCE, which bounds the operator's
// bucket bill.
func TestUploadIsRefusedWhenTheAccountFilledUpWhileItStreamed(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 1000
	svc := NewService(repo, store, cfg)
	ctx := context.Background()

	accountID, imageID := uuid.New(), uuid.New()
	if err := repo.Create(ctx, &image.Image{ID: imageID, AccountID: accountID}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Another image consumed the account's room while this upload streamed.
	filler := &image.Image{ID: uuid.New(), AccountID: accountID}
	filler.SetFileStorageInfo("local", "acct/filler.img", "filler.img", 900)
	if err := repo.Create(ctx, filler); err != nil {
		t.Fatalf("seed filler: %v", err)
	}

	body := strings.NewReader(strings.Repeat("x", 500))
	_, err := svc.UploadFile(ctx, image_upload.ImageUploadCommand{
		ID:          imageID,
		FileName:    "disk.img",
		SizeBytes:   500,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}, body)

	if !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("UploadFile = %v, want ErrImageLimitReached", err)
	}
	if _, ok := store.files[accountID.String()+"/"+imageID.String()+".img"]; ok {
		t.Error("the refused upload left its bytes in storage, which nothing will ever name again")
	}
	stored, _ := repo.GetByID(ctx, accountID, imageID)
	if stored.FileStorageInfo != nil {
		t.Error("the refused upload was recorded on the row")
	}
}

// An upload that fits must still work: the re-check is a ceiling, not a ban.
func TestUploadStillSucceedsWithinTheCeiling(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 1000
	svc := NewService(repo, store, cfg)
	ctx := context.Background()

	accountID, imageID := uuid.New(), uuid.New()
	if err := repo.Create(ctx, &image.Image{ID: imageID, AccountID: accountID}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	body := strings.NewReader(strings.Repeat("x", 500))
	updated, err := svc.UploadFile(ctx, image_upload.ImageUploadCommand{
		ID:          imageID,
		FileName:    "disk.img",
		SizeBytes:   500,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}, body)
	if err != nil {
		t.Fatalf("UploadFile = %v, want nil", err)
	}
	if updated.FileStorageInfo == nil || updated.FileStorageInfo.SizeBytes != 500 {
		t.Fatalf("file storage info = %v, want 500 bytes", updated.FileStorageInfo)
	}
}

// A failed upload into a capture record must leave it retryable.
//
// ClaimUpload is allowed from any non-busy status, capture_failed included, and
// ReleaseUpload wrote "". The agent's retry goes through ClaimCaptureUpload,
// which accepts only `capturing` or `capture_failed`, so it was refused
// permanently — while quotaOccupyingScope still counted the row, costing one of
// the account's image slots. A dead slot with no file and no way back.
func TestAFailedUploadIntoACaptureRecordLeavesItRetryable(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	store.storeErr = errors.New("disk full")
	svc := NewService(repo, store, &config.Config{})
	ctx := context.Background()

	accountID, imageID, sourceNode := uuid.New(), uuid.New(), uuid.New()
	if err := repo.Create(ctx, &image.Image{
		ID:                imageID,
		AccountID:         accountID,
		Kind:              image.KindBackup,
		SourceNodeID:      &sourceNode,
		CompressionStatus: image.CaptureFailed,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := svc.UploadFile(ctx, image_upload.ImageUploadCommand{
		ID:          imageID,
		FileName:    "disk.img",
		SizeBytes:   4,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}, strings.NewReader("xxxx"))
	if err == nil {
		t.Fatal("UploadFile succeeded against a storage error")
	}

	stored, _ := repo.GetByID(ctx, accountID, imageID)
	if stored.CompressionStatus != image.CaptureFailed {
		t.Fatalf("status = %q after a failed upload into a capture record, want %q — the agent's retry is now refused forever and the row still spends a slot",
			stored.CompressionStatus, image.CaptureFailed)
	}
	claimed, err := repo.ClaimCaptureUpload(ctx, imageID)
	if err != nil || !claimed {
		t.Fatalf("ClaimCaptureUpload = %v, %v; the record must still accept the agent's retry", claimed, err)
	}
}

// An ordinary image record must still come back to idle, not to a capture state
// it has no business being in.
func TestAFailedUploadIntoAnOrdinaryRecordClearsTheClaim(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	store.storeErr = errors.New("disk full")
	svc := NewService(repo, store, &config.Config{})
	ctx := context.Background()

	accountID, imageID := uuid.New(), uuid.New()
	if err := repo.Create(ctx, &image.Image{ID: imageID, AccountID: accountID}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := svc.UploadFile(ctx, image_upload.ImageUploadCommand{
		ID:          imageID,
		FileName:    "disk.img",
		SizeBytes:   4,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}, strings.NewReader("xxxx")); err == nil {
		t.Fatal("UploadFile succeeded against a storage error")
	}

	stored, _ := repo.GetByID(ctx, accountID, imageID)
	if stored.CompressionStatus != "" {
		t.Fatalf("status = %q, want empty", stored.CompressionStatus)
	}
}

// A capture whose finalize write fails must not leave the row charging the
// account for bytes it has just deleted.
//
// failCapture deletes the stored object, but if the copy it then writes still
// carries the FileStorageInfo the finalize has just set, the row lands in
// capture_failed with size_bytes for an object that no longer exists;
// SumBytesByAccountID deliberately does not exclude failed captures and the
// startup reset rewrites only compression_status, so nothing would clear it.
// The account would pay for that capture until the record was deleted by hand.
func TestAFailedFinalizeDoesNotChargeForDeletedBytes(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	cfg := &config.Config{}
	cfg.JWT.SecretKey = "test-secret-key-for-capture-uploads"
	svc := NewService(repo, store, cfg)
	ctx := context.Background()

	accountID := uuid.New()
	capturing := image.NewCaptureImage(uuid.New(), accountID, "backup", false, uuid.New(), "node-1")
	if err := repo.Create(ctx, capturing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rawURL, err := svc.CaptureUploadURL(ctx, accountID, capturing.ID, time.Hour)
	if err != nil {
		t.Fatalf("CaptureUploadURL: %v", err)
	}
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}

	// Only the FINALIZE write fails. failCapture's own write must land, or the
	// test observes nothing: with both failing, the record simply keeps whatever
	// it was seeded with and the assertion passes against the defect.
	repo.captureWriteErrOnce = errors.New("connection reset")
	_, err = svc.FinalizeCapture(ctx, capturing.ID, parsed.Query().Get("exp"), parsed.Query().Get("sig"),
		strings.NewReader("captured-bytes"))
	if err == nil {
		t.Fatal("FinalizeCapture succeeded against a write error")
	}

	stored, _ := repo.GetByIDGlobal(ctx, capturing.ID)
	if stored.FileStorageInfo != nil {
		t.Fatalf("the failed capture still records %d bytes for an object it deleted; the account is charged for it forever",
			stored.FileStorageInfo.SizeBytes)
	}
	if stored.Checksum != "" {
		t.Errorf("checksum = %q, want empty: it describes bytes that no longer exist", stored.Checksum)
	}
	if used, err := repo.SumBytesByAccountID(ctx, accountID, nil); err != nil || used != 0 {
		t.Errorf("account byte total = %d (err %v), want 0", used, err)
	}
}

// Deleting an image must remove its objects even when the client has gone away.
//
// The row deletion commits first and the objects go best-effort on the REQUEST
// context, so a client that navigates away would leave up to two multi-GB objects
// with no row: invisible to the byte quota, to SumBytesAllAccounts and to an
// out-of-band account purge that works from rows. The release paths in the same
// file are explicitly detached for exactly this reason, and the compression
// artifacts have a startup sweep for this orphan class; the ordinary delete
// path — the one operators actually use — must be detached too.
func TestDeleteRemovesTheObjectsEvenIfTheClientIsGone(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	svc := NewService(repo, store, &config.Config{})

	accountID, imageID := uuid.New(), uuid.New()
	img := &image.Image{ID: imageID, AccountID: accountID}
	img.SetFileStorageInfo("local", "acct/img.img.xz", "img.img.xz", 40)
	img.FileStorageInfo.SourceFilePath = "acct/img.img"
	img.FileStorageInfo.SourceSizeBytes = 400
	if err := repo.Create(context.Background(), img); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.files["acct/img.img.xz"] = []byte("compressed")
	store.files["acct/img.img"] = []byte("original")

	// The client is already gone by the time the delete runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := svc.Delete(ctx, accountID, imageID); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	for _, key := range []string{"acct/img.img.xz", "acct/img.img"} {
		if _, ok := store.files[key]; ok {
			t.Errorf("%s survived the delete; its row is gone, so nothing will ever name it again", key)
		}
	}
}
