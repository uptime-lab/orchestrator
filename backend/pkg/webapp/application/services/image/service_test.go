package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	neturl "net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_compress"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_upload"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/google/uuid"
)

// fakeImageRepo is an in-memory ImageRepository for tests.
type fakeImageRepo struct {
	mu          sync.Mutex // the concurrency-cap test drives this from several goroutines
	images      map[uuid.UUID]*image.Image
	updateErr   error
	progressLog []int
	// captureWriteErrOnce fails the NEXT UpdateIfCaptureUploading and then
	// clears itself, so a test can fail the finalize write while letting the
	// settle that follows it land — which is the only way to observe what
	// failCapture actually stores.
	captureWriteErrOnce error
	// afterGet runs at the end of every GetByID. It is how a test puts a
	// concurrent claim BETWEEN a service's read and its write, which is the
	// window every claim in this file exists for and which no test could
	// otherwise reach.
	afterGet func()
	// refuseBusyDelete mirrors the SQL Delete's predicate: a record a job holds
	// is not deleted. Off by default because the purge tests delete the row
	// out from under a running job on purpose.
	refuseBusyDelete bool
}

func newFakeImageRepo() *fakeImageRepo {
	return &fakeImageRepo{images: map[uuid.UUID]*image.Image{}}
}

func (r *fakeImageRepo) Create(ctx context.Context, img *image.Image) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := *img
	r.images[img.ID] = &copied
	return nil
}

// CountQuotaOccupying mirrors quotaOccupyingScope in the SQL repository: a
// failed capture never counts, an in-progress one counts only while it is
// younger than inFlight, everything else always counts. It is a fake and not
// SQL, so the SERIALIZATION it cannot model is covered by the Postgres
// integration test — what this reproduces is the counting rule.
func (r *fakeImageRepo) CountQuotaOccupying(
	_ context.Context,
	accountID uuid.UUID,
	inFlight time.Duration,
	excludeImageID *uuid.UUID,
) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var count int64
	for id, img := range r.images {
		if img.AccountID != accountID {
			continue
		}
		if excludeImageID != nil && id == *excludeImageID {
			continue
		}
		switch img.CompressionStatus {
		case image.CaptureFailed:
			continue
		case image.CaptureInProgress:
			if inFlight <= 0 || !img.CreatedAt.After(time.Now().Add(-inFlight)) {
				continue
			}
		}
		count++
	}
	return count, nil
}

func (r *fakeImageRepo) CreateCaptureUnderCap(
	ctx context.Context,
	img *image.Image,
	maxPerAccount int,
	inFlight time.Duration,
) error {
	if maxPerAccount > 0 {
		count, err := r.CountQuotaOccupying(ctx, img.AccountID, inFlight, nil)
		if err != nil {
			return err
		}
		if count >= int64(maxPerAccount) {
			return fmt.Errorf("%w (max %d)", app_errors.ErrImageLimitReached, maxPerAccount)
		}
	}
	return r.Create(ctx, img)
}

// CreateUnderCap mirrors the SQL repository's checks (count cap, account and
// instance byte ceilings). The SERIALIZATION a fake cannot model is covered by
// the Postgres integration test; what this reproduces is the refusal rules.
func (r *fakeImageRepo) CreateUnderCap(
	ctx context.Context,
	img *image.Image,
	maxPerAccount int,
	inFlight time.Duration,
	maxBytesPerAccount int64,
	maxBytesInstance int64,
) error {
	if maxPerAccount > 0 {
		count, err := r.CountQuotaOccupying(ctx, img.AccountID, inFlight, nil)
		if err != nil {
			return err
		}
		if count >= int64(maxPerAccount) {
			return fmt.Errorf("%w (max %d)", app_errors.ErrImageLimitReached, maxPerAccount)
		}
	}
	if maxBytesPerAccount > 0 {
		used, err := r.SumBytesByAccountID(ctx, img.AccountID, nil)
		if err != nil {
			return err
		}
		if used >= maxBytesPerAccount {
			return fmt.Errorf("%w: the stored image quota is full — delete an image to free space",
				app_errors.ErrImageLimitReached)
		}
	}
	if maxBytesInstance > 0 {
		used, err := r.SumBytesAllAccounts(ctx)
		if err != nil {
			return err
		}
		if used >= maxBytesInstance {
			return fmt.Errorf("%w: the stored image quota is full — delete an image to free space",
				app_errors.ErrImageLimitReached)
		}
	}
	return r.Create(ctx, img)
}

// The two byte sums mirror the SQL: current file PLUS a retained
// pre-compression original, since both sit in storage and both are billed.
func (r *fakeImageRepo) SumBytesByAccountID(_ context.Context, accountID uuid.UUID, excludeImageID *uuid.UUID) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for id, img := range r.images {
		if img.AccountID != accountID {
			continue
		}
		if excludeImageID != nil && id == *excludeImageID {
			continue
		}
		total += storedBytesOf(img)
	}
	return total, nil
}

func (r *fakeImageRepo) SumBytesAllAccounts(_ context.Context) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, img := range r.images {
		total += storedBytesOf(img)
	}
	return total, nil
}

func (r *fakeImageRepo) Update(ctx context.Context, img *image.Image) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	copied := *img
	r.images[img.ID] = &copied
	return nil
}

// UpdateMetadata mirrors the SQL: FOUR columns, leaving compression_status,
// file_storage_info and checksum exactly as stored. A fake that copied the whole
// record would hide the very thing this method exists to prevent.
func (r *fakeImageRepo) UpdateMetadata(_ context.Context, accountID, imageID uuid.UUID, title, version, description, imageURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	stored, ok := r.images[imageID]
	if !ok || stored.AccountID != accountID {
		return app_errors.ErrImageNotFound
	}
	stored.Title = title
	stored.Version = version
	stored.Description = description
	stored.ImageURL = imageURL
	stored.UpdatedAt = time.Now()
	return nil
}

// ClaimUpload mirrors the SQL predicate rather than passing through: a fake
// looser than production converts a defect into a passing test.
func (r *fakeImageRepo) ClaimUpload(ctx context.Context, accountID, imageID uuid.UUID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.images[imageID]
	if !ok || stored.AccountID != accountID {
		return false, app_errors.ErrImageNotFound
	}
	if image.IsBusy(stored.CompressionStatus) {
		return false, nil
	}
	stored.CompressionStatus = image.UploadInProgress
	return true, nil
}

// ClaimCaptureUpload mirrors the SQL predicate (claimable only from the two
// idle capture states) rather than passing through — a fake looser than
// production converts a defect into a passing test.
func (r *fakeImageRepo) ClaimCaptureUpload(ctx context.Context, imageID uuid.UUID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.images[imageID]
	if !ok {
		return false, app_errors.ErrImageNotFound
	}
	if stored.CompressionStatus != image.CaptureInProgress && stored.CompressionStatus != image.CaptureFailed {
		return false, nil
	}
	stored.CompressionStatus = image.CaptureUploadInProgress
	return true, nil
}

// FailAwaitingCapture mirrors the SQL predicate: only a record still awaiting
// its stream moves.
func (r *fakeImageRepo) FailAwaitingCapture(ctx context.Context, accountID, imageID uuid.UUID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.images[imageID]
	if !ok || stored.AccountID != accountID || stored.CompressionStatus != image.CaptureInProgress {
		return false, nil
	}
	stored.CompressionStatus = image.CaptureFailed
	return true, nil
}

func (r *fakeImageRepo) UpdateIfCaptureUploading(ctx context.Context, img *image.Image) (bool, error) {
	// Mirrors database/sql: a cancelled context never executes the statement.
	// The service calls this on a DETACHED context for exactly that reason, and
	// a fake that ignores cancellation would pass against code that does not.
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.captureWriteErrOnce != nil {
		err := r.captureWriteErrOnce
		r.captureWriteErrOnce = nil
		return false, err
	}
	if r.updateErr != nil {
		return false, r.updateErr
	}
	stored, ok := r.images[img.ID]
	if !ok || stored.AccountID != img.AccountID {
		return false, nil
	}
	if stored.CompressionStatus != image.CaptureUploadInProgress {
		return false, nil
	}
	copied := *img
	r.images[img.ID] = &copied
	return true, nil
}

func (r *fakeImageRepo) ListStaleCompressions(ctx context.Context) ([]image.Image, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []image.Image
	for _, img := range r.images {
		if img.CompressionStatus == image.CompressionInProgress {
			out = append(out, *img)
		}
	}
	return out, nil
}

func (r *fakeImageRepo) ReleaseUpload(ctx context.Context, accountID, imageID uuid.UUID) error {
	// Mirrors database/sql, which checks the context BEFORE acquiring a
	// connection and never executes the statement on a cancelled one. Without
	// this the fake is more permissive than production and a test of "the claim
	// is released even when the request is gone" passes against code that does
	// not release it — the exact way a green suite hides a defect.
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.images[imageID]
	if !ok || stored.AccountID != accountID {
		return nil
	}
	if stored.CompressionStatus == image.UploadInProgress {
		// Mirrors the SQL CASE: a record with a source node and no stored file
		// is a capture that never landed, and capture_failed is the only status
		// that keeps it retryable. Anything else is idle.
		if stored.SourceNodeID != nil && stored.FileStorageInfo == nil {
			stored.CompressionStatus = image.CaptureFailed
		} else {
			stored.CompressionStatus = ""
		}
	}
	return nil
}

func (r *fakeImageRepo) GetByID(ctx context.Context, accountID, imageID uuid.UUID) (*image.Image, error) {
	r.mu.Lock()
	img, ok := r.images[imageID]
	if !ok || img.AccountID != accountID {
		r.mu.Unlock()
		return nil, app_errors.ErrImageNotFound
	}
	copied := *img
	hook := r.afterGet
	r.mu.Unlock()
	// Outside the lock: the hook exists to run another repository call.
	if hook != nil {
		hook()
	}
	return &copied, nil
}

func (r *fakeImageRepo) GetByIDGlobal(ctx context.Context, imageID uuid.UUID) (*image.Image, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	img, ok := r.images[imageID]
	if !ok {
		return nil, app_errors.ErrImageNotFound
	}
	copied := *img
	return &copied, nil
}

func (r *fakeImageRepo) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]image.Image, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []image.Image
	for _, img := range r.images {
		if img.AccountID == accountID {
			out = append(out, *img)
		}
	}
	return out, nil
}

func (r *fakeImageRepo) Delete(ctx context.Context, accountID, imageID uuid.UUID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if img, ok := r.images[imageID]; ok && r.refuseBusyDelete &&
		(img.CompressionStatus == image.CompressionInProgress ||
			img.CompressionStatus == image.UploadInProgress ||
			img.CompressionStatus == image.CaptureUploadInProgress) {
		return app_errors.ErrImageCompressionInProgress
	}
	delete(r.images, imageID)
	return nil
}

func (r *fakeImageRepo) ClaimCompression(ctx context.Context, accountID, imageID uuid.UUID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	img, ok := r.images[imageID]
	if !ok || img.AccountID != accountID {
		return false, app_errors.ErrImageNotFound
	}
	// Mirrors the SQL predicate: an in-flight UPLOAD holds the record too.
	if img.CompressionStatus == image.CompressionInProgress ||
		img.CompressionStatus == image.UploadInProgress {
		return false, nil
	}
	img.CompressionStatus = image.CompressionInProgress
	img.CompressionProgress = 0
	return true, nil
}

func (r *fakeImageRepo) SetCompressionState(ctx context.Context, accountID, imageID uuid.UUID, status string, progress int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	img, ok := r.images[imageID]
	if !ok || img.AccountID != accountID {
		return app_errors.ErrImageNotFound
	}
	img.CompressionStatus = status
	img.CompressionProgress = progress
	r.progressLog = append(r.progressLog, progress)
	return nil
}

func (r *fakeImageRepo) ResetInProgressCompression(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, img := range r.images {
		if img.CompressionStatus == image.CompressionInProgress {
			img.CompressionStatus = image.CompressionFailed
		}
	}
	return nil
}

// fakeStorage is an in-memory StorageClient for tests.
type fakeStorage struct {
	mu       sync.Mutex // the concurrency-cap test drives this from several goroutines
	files    map[string][]byte
	storeErr error
	// truncateTo, when >= 0, stores only the first N bytes to simulate a
	// short write without failing the store call.
	truncateTo int64
	// onStore runs once the object has been written but before the caller
	// continues. It is how a test puts something in the WINDOW a real upload
	// spends streaming — the window the upload claim closes — without threading
	// goroutines and synchronisation through every other test.
	onStore func(name string)
	// lastOptions records what the most recent store was asked to persist on
	// the object (content type, metadata), for tests about what reaches the
	// bucket rather than what reaches the record.
	lastOptions *storage.StorageOptions
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{files: map[string][]byte{}, truncateTo: -1}
}

func (s *fakeStorage) StoreWithOptions(ctx context.Context, name string, content io.Reader, options *storage.StorageOptions) (string, error) {
	s.mu.Lock()
	s.lastOptions = options
	s.mu.Unlock()
	if s.storeErr != nil {
		return "", s.storeErr
	}
	reader := content
	if s.truncateTo >= 0 {
		reader = io.LimitReader(content, s.truncateTo)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	// Lock only the map write: ReadAll blocks on the compressor pipe, and
	// holding the mutex across it would serialize the very concurrency the
	// cap test needs to observe.
	s.mu.Lock()
	s.files[name] = data
	hook := s.onStore
	s.mu.Unlock()
	if hook != nil {
		hook(name)
	}
	// Mirrors an object-storage backend, which aborts a transfer on a cancelled
	// context. A fake that ignores cancellation lets an upload "succeed" after
	// its client is gone, which hides every question about what the failure path
	// leaves behind.
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return name, nil
}

func (s *fakeStorage) Retrieve(ctx context.Context, name string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.files[name]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", name)
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (s *fakeStorage) GetSignedURL(ctx context.Context, name string, expiration time.Duration) (string, error) {
	return "https://example.test/" + name, nil
}

func (s *fakeStorage) Delete(ctx context.Context, name string) error {
	// Honours cancellation, like every real client: the S3 SDK aborts on a dead
	// context and so does a filesystem write through database/sql's discipline.
	// A fake that ignored it would pass against code that deletes objects on the
	// REQUEST context, which is the whole class of orphan this file now guards.
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, name)
	return nil
}

func (s *fakeStorage) Exists(ctx context.Context, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.files[name]
	return ok, nil
}

func (s *fakeStorage) GetFileInfo(ctx context.Context, name string) (*file.FileInfo, error) {
	return nil, nil
}


func (s *fakeStorage) GetStorageType() string { return "local" }

func setupService(t *testing.T) (*Service, *fakeImageRepo, *fakeStorage, image_upload.ImageUploadCommand) {
	t.Helper()
	repo := newFakeImageRepo()
	store := newFakeStorage()
	svc := NewService(repo, store, &config.Config{})

	accountID := uuid.New()
	imageID := uuid.New()
	img := &image.Image{
		ID:        imageID,
		AccountID: accountID,
		Title:     "Ubuntu",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := repo.Create(context.Background(), img); err != nil {
		t.Fatalf("failed to seed image: %v", err)
	}

	cmd := image_upload.ImageUploadCommand{
		ID:          imageID,
		FileName:    "ubuntu-24.04.img",
		ContentType: "application/octet-stream",
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}
	return svc, repo, store, cmd
}

func TestUploadFileSuccess(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	content := "raspberry pi os image bytes"
	cmd.SizeBytes = int64(len(content))

	img, err := svc.UploadFile(context.Background(), cmd, strings.NewReader(content))
	if err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	if img.FileStorageInfo == nil {
		t.Fatal("expected FileStorageInfo to be set")
	}
	wantPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	if img.FileStorageInfo.FilePath != wantPath {
		t.Errorf("file path = %q, want %q", img.FileStorageInfo.FilePath, wantPath)
	}
	if img.FileStorageInfo.FileName != cmd.FileName {
		t.Errorf("file name = %q, want %q", img.FileStorageInfo.FileName, cmd.FileName)
	}
	if img.FileStorageInfo.SizeBytes != int64(len(content)) {
		t.Errorf("size = %d, want %d", img.FileStorageInfo.SizeBytes, len(content))
	}

	sum := sha256.Sum256([]byte(content))
	if img.Checksum != hex.EncodeToString(sum[:]) {
		t.Errorf("checksum = %q, want %q", img.Checksum, hex.EncodeToString(sum[:]))
	}

	if string(store.files[wantPath]) != content {
		t.Error("stored file content does not match uploaded content")
	}

	persisted, _ := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if persisted.FileStorageInfo == nil || persisted.FileStorageInfo.FilePath != wantPath {
		t.Error("repository was not updated with file storage info")
	}
}

func TestUploadFileValidation(t *testing.T) {
	svc, _, _, cmd := setupService(t)
	cmd.FileName = "notes.txt"
	cmd.SizeBytes = 10

	_, err := svc.UploadFile(context.Background(), cmd, strings.NewReader("0123456789"))
	if !errors.Is(err, app_errors.ErrInvalidImageFile) {
		t.Fatalf("expected ErrInvalidImageFile, got %v", err)
	}
}

func TestUploadFileKeepsOldFileOnStorageFailure(t *testing.T) {
	svc, repo, store, cmd := setupService(t)

	// Seed a previously uploaded file.
	oldContent := "old image"
	oldCmd := cmd
	oldCmd.FileName = "old.iso"
	oldCmd.SizeBytes = int64(len(oldContent))
	if _, err := svc.UploadFile(context.Background(), oldCmd, strings.NewReader(oldContent)); err != nil {
		t.Fatalf("seeding old file failed: %v", err)
	}
	oldPath := fmt.Sprintf("%s/%s.iso", cmd.AuthContext.AccountID, cmd.ID)

	store.storeErr = errors.New("disk full")
	cmd.SizeBytes = 10
	_, err := svc.UploadFile(context.Background(), cmd, strings.NewReader("0123456789"))
	if err == nil {
		t.Fatal("expected upload to fail")
	}

	if _, ok := store.files[oldPath]; !ok {
		t.Error("old file was removed even though the new upload failed")
	}
	persisted, _ := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if persisted.FileStorageInfo == nil || persisted.FileStorageInfo.FilePath != oldPath {
		t.Error("image record no longer points at the old file after a failed upload")
	}
}

func TestUploadFileDeletesOldFileAfterSuccess(t *testing.T) {
	svc, _, store, cmd := setupService(t)

	oldContent := "old image"
	oldCmd := cmd
	oldCmd.FileName = "old.iso"
	oldCmd.SizeBytes = int64(len(oldContent))
	if _, err := svc.UploadFile(context.Background(), oldCmd, strings.NewReader(oldContent)); err != nil {
		t.Fatalf("seeding old file failed: %v", err)
	}
	oldPath := fmt.Sprintf("%s/%s.iso", cmd.AuthContext.AccountID, cmd.ID)

	newContent := "new image"
	cmd.SizeBytes = int64(len(newContent))
	if _, err := svc.UploadFile(context.Background(), cmd, strings.NewReader(newContent)); err != nil {
		t.Fatalf("upload failed: %v", err)
	}

	if _, ok := store.files[oldPath]; ok {
		t.Error("old file with a different extension was not deleted after a successful upload")
	}
	newPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	if string(store.files[newPath]) != newContent {
		t.Error("new file content does not match")
	}
}

func TestUploadFileSizeMismatch(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	content := "0123456789"
	cmd.SizeBytes = int64(len(content)) + 5 // declared size larger than actual

	_, err := svc.UploadFile(context.Background(), cmd, strings.NewReader(content))
	if err == nil || !strings.Contains(err.Error(), "does not match declared size") {
		t.Fatalf("expected size mismatch error, got %v", err)
	}

	newPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	if _, ok := store.files[newPath]; ok {
		t.Error("truncated file was not cleaned up")
	}
	persisted, _ := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if persisted.FileStorageInfo != nil {
		t.Error("image record must not reference a truncated file")
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeCompressor "compresses" by prefixing the payload, so tests can verify
// the swap logic without a real xz binary.
type fakeCompressor struct {
	fail bool
	ext  string // defaults to "xz"
}

func (f *fakeCompressor) Compress(ctx context.Context, dst io.Writer, src io.Reader) error {
	if f.fail {
		return errors.New("compressor exploded")
	}
	if _, err := dst.Write([]byte("XZ:")); err != nil {
		return err
	}
	_, err := io.Copy(dst, src)
	return err
}

func (f *fakeCompressor) Extension() string {
	if f.ext != "" {
		return f.ext
	}
	return "xz"
}
func (f *fakeCompressor) ContentType() string { return "application/x-xz" }

// fakeDecompressor strips the "COMP:" prefix, mirroring a compressed source.
type fakeDecompressor struct{}

func (f *fakeDecompressor) Decompress(ctx context.Context, dst io.Writer, src io.Reader) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(data), "COMP:") {
		return errors.New("source is not fake-compressed")
	}
	_, err = dst.Write([]byte(strings.TrimPrefix(string(data), "COMP:")))
	return err
}

func (f *fakeDecompressor) Extension() string { return "xz" }

func seedRawUpload(t *testing.T, svc *Service, cmd image_upload.ImageUploadCommand, content string) {
	t.Helper()
	c := cmd
	c.SizeBytes = int64(len(content))
	if _, err := svc.UploadFile(context.Background(), c, strings.NewReader(content)); err != nil {
		t.Fatalf("seeding raw upload failed: %v", err)
	}
}

func TestStartCompressionValidation(t *testing.T) {
	svc, repo, _, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	compressCmd := image_compress.ImageCompressCommand{ID: cmd.ID, AuthContext: cmd.AuthContext}

	// No file uploaded yet.
	if _, err := svc.StartCompression(context.Background(), compressCmd); !errors.Is(err, app_errors.ErrInvalidImageFile) {
		t.Fatalf("expected ErrInvalidImageFile for missing file, got %v", err)
	}

	// Already compressed (xz and zstd alike).
	xzCmd := cmd
	xzCmd.FileName = "ubuntu.img.xz"
	seedRawUpload(t, svc, xzCmd, "compressed bytes")
	if _, err := svc.StartCompression(context.Background(), compressCmd); !errors.Is(err, app_errors.ErrInvalidImageFile) {
		t.Fatalf("expected ErrInvalidImageFile for .xz file, got %v", err)
	}
	zstCmd := cmd
	zstCmd.FileName = "ubuntu.img.zst"
	seedRawUpload(t, svc, zstCmd, "compressed bytes")
	if _, err := svc.StartCompression(context.Background(), compressCmd); !errors.Is(err, app_errors.ErrInvalidImageFile) {
		t.Fatalf("expected ErrInvalidImageFile for .zst file, got %v", err)
	}

	// Compression already running.
	seedRawUpload(t, svc, cmd, "raw bytes")
	img, _ := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	img.CompressionStatus = image.CompressionInProgress
	if err := repo.Update(context.Background(), img); err != nil {
		t.Fatalf("failed to seed status: %v", err)
	}
	if _, err := svc.StartCompression(context.Background(), compressCmd); !errors.Is(err, app_errors.ErrImageCompressionInProgress) {
		t.Fatalf("expected ErrImageCompressionInProgress, got %v", err)
	}
}

func TestRunCompressionSwapsFileAndChecksum(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	content := "raw image payload"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)

	// Run the job synchronously, as StartCompression's goroutine would.
	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, rawPath, cmd.FileName, int64(len(content)), "")

	img, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}

	compressedPath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)
	if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath != compressedPath {
		t.Fatalf("image does not point at compressed file: %+v", img.FileStorageInfo)
	}
	if img.FileStorageInfo.FileName != cmd.FileName+".xz" {
		t.Errorf("file name = %q, want %q", img.FileStorageInfo.FileName, cmd.FileName+".xz")
	}
	if img.CompressionStatus != "" {
		t.Errorf("compression status = %q, want empty", img.CompressionStatus)
	}

	wantContent := "XZ:" + content
	if string(store.files[compressedPath]) != wantContent {
		t.Error("compressed file content mismatch")
	}
	if img.FileStorageInfo.SizeBytes != int64(len(wantContent)) {
		t.Errorf("size = %d, want %d", img.FileStorageInfo.SizeBytes, len(wantContent))
	}
	sum := sha256.Sum256([]byte(wantContent))
	if img.Checksum != hex.EncodeToString(sum[:]) {
		t.Error("checksum must cover the compressed file")
	}
	// The original is retained as a safety copy and referenced on the record.
	if string(store.files[rawPath]) != content {
		t.Error("original file must be kept in storage after compression")
	}
	if img.FileStorageInfo.SourceFilePath != rawPath {
		t.Errorf("source path = %q, want %q", img.FileStorageInfo.SourceFilePath, rawPath)
	}
	if img.FileStorageInfo.SourceSizeBytes != int64(len(content)) {
		t.Errorf("source size = %d, want %d", img.FileStorageInfo.SourceSizeBytes, len(content))
	}
	if img.CompressionProgress != 100 {
		t.Errorf("compression progress = %d, want 100", img.CompressionProgress)
	}
	// Live progress is capped at 99 (input is consumed before xz finishes);
	// the final 100 lands only with the record swap, checked above.
	for _, p := range repo.progressLog {
		if p > 99 {
			t.Errorf("progress log %v must never exceed 99 before the swap", repo.progressLog)
		}
	}
}

func TestRunCompressionFailureKeepsRawFile(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{fail: true}, testLogger())

	content := "raw image payload"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)

	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, rawPath, cmd.FileName, int64(len(content)), "")

	img, _ := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if img.CompressionStatus != image.CompressionFailed {
		t.Errorf("compression status = %q, want %q", img.CompressionStatus, image.CompressionFailed)
	}
	if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath != rawPath {
		t.Error("image must still point at the raw file after a failed compression")
	}
	if string(store.files[rawPath]) != content {
		t.Error("raw file content must be untouched")
	}
	compressedPath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)
	if _, ok := store.files[compressedPath]; ok {
		t.Error("partial compressed file was not cleaned up")
	}
}

func TestUploadRejectedWhileCompressionInProgress(t *testing.T) {
	svc, repo, _, cmd := setupService(t)
	seedRawUpload(t, svc, cmd, "raw bytes")

	img, _ := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	img.CompressionStatus = image.CompressionInProgress
	if err := repo.Update(context.Background(), img); err != nil {
		t.Fatalf("failed to seed status: %v", err)
	}

	cmd.SizeBytes = 10
	_, err := svc.UploadFile(context.Background(), cmd, strings.NewReader("0123456789"))
	if !errors.Is(err, app_errors.ErrImageCompressionInProgress) {
		t.Fatalf("expected ErrImageCompressionInProgress, got %v", err)
	}
}

func TestRunCompressionTranscodesXZSource(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{ext: "zst"}, testLogger())
	svc.SetDecompressors(&fakeDecompressor{})

	raw := "raw image payload"
	xzCmd := cmd
	xzCmd.FileName = "ubuntu.img.xz"
	seedRawUpload(t, svc, xzCmd, "COMP:"+raw)
	sourcePath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)

	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, sourcePath, "ubuntu.img.xz", int64(len("COMP:"+raw)), "")

	img, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	zstPath := fmt.Sprintf("%s/%s.zst", cmd.AuthContext.AccountID, cmd.ID)
	if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath != zstPath {
		t.Fatalf("image does not point at transcoded file: %+v", img.FileStorageInfo)
	}
	if img.FileStorageInfo.FileName != "ubuntu.img.zst" {
		t.Errorf("file name = %q, want ubuntu.img.zst", img.FileStorageInfo.FileName)
	}
	// Transcode = decompress (strip COMP:) then compress (prefix XZ:).
	if string(store.files[zstPath]) != "XZ:"+raw {
		t.Errorf("transcoded content = %q, want %q", store.files[zstPath], "XZ:"+raw)
	}
	if string(store.files[sourcePath]) != "COMP:"+raw {
		t.Error("xz source must be kept in storage after transcoding")
	}
	if img.FileStorageInfo.SourceFilePath != sourcePath {
		t.Errorf("source path = %q, want %q", img.FileStorageInfo.SourceFilePath, sourcePath)
	}
}

func TestRunCompressionSameExtensionRepackKeepsOriginal(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	// Deploy format xz, source is an uploaded (single-block) .xz: the output
	// path collides with the source, so the original must be preserved under
	// a distinct .orig name.
	svc.SetCompressor(&fakeCompressor{ext: "xz"}, testLogger())
	svc.SetDecompressors(&fakeDecompressor{})

	raw := "raw image payload"
	xzCmd := cmd
	xzCmd.FileName = "official.img.xz"
	seedRawUpload(t, svc, xzCmd, "COMP:"+raw)
	sourcePath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)
	origPath := fmt.Sprintf("%s/%s.orig.xz", cmd.AuthContext.AccountID, cmd.ID)

	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, sourcePath, "official.img.xz", int64(len("COMP:"+raw)), "")

	img, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	if img.FileStorageInfo.FilePath != sourcePath {
		t.Errorf("file path = %q, want %q (same-ext output)", img.FileStorageInfo.FilePath, sourcePath)
	}
	if string(store.files[sourcePath]) != "XZ:"+raw {
		t.Errorf("repacked content = %q, want %q", store.files[sourcePath], "XZ:"+raw)
	}
	if string(store.files[origPath]) != "COMP:"+raw {
		t.Error("original must be preserved at the .orig path")
	}
	if img.FileStorageInfo.SourceFilePath != origPath {
		t.Errorf("source path = %q, want %q", img.FileStorageInfo.SourceFilePath, origPath)
	}
	if img.FileStorageInfo.FileName != "official.img.xz" {
		t.Errorf("file name = %q, want official.img.xz", img.FileStorageInfo.FileName)
	}

	// A second compression attempt must now be rejected: the file already
	// references its retained source.
	compressCmd := image_compress.ImageCompressCommand{ID: cmd.ID, AuthContext: cmd.AuthContext}
	if _, err := svc.StartCompression(context.Background(), compressCmd); !errors.Is(err, app_errors.ErrInvalidImageFile) {
		t.Fatalf("expected ErrInvalidImageFile for already-repacked file, got %v", err)
	}
}

// A same-path repack replaces the original at its key before the record is
// swapped. A failure in between — here the record write; equally the
// commit-time quota re-check or the deadline — must put the original back:
// the record still carries the original's checksum and size, so leaving the
// repacked bytes there fails every install's digest check, and deleting the
// staged copy would lose the only copy of what the record describes.
func TestARepackThatFailsAfterTheStoreRestoresTheOriginal(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{ext: "xz"}, testLogger())
	svc.SetDecompressors(&fakeDecompressor{})

	raw := "raw image payload"
	xzCmd := cmd
	xzCmd.FileName = "official.img.xz"
	seedRawUpload(t, svc, xzCmd, "COMP:"+raw)
	sourcePath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)
	origPath := fmt.Sprintf("%s/%s.orig.xz", cmd.AuthContext.AccountID, cmd.ID)
	before, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	repo.updateErr = errors.New("database unavailable")

	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, sourcePath, "official.img.xz", int64(len("COMP:"+raw)), before.Checksum)

	if got := string(store.files[sourcePath]); got != "COMP:"+raw {
		t.Errorf("content at the record's key = %q, want the original %q back", got, "COMP:"+raw)
	}
	if _, ok := store.files[origPath]; ok {
		t.Error("the staged copy was left behind after the original was restored")
	}
	after, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	if after.Checksum != before.Checksum || after.FileStorageInfo.FilePath != sourcePath {
		t.Errorf("record moved: checksum %q→%q, path %q", before.Checksum, after.Checksum, after.FileStorageInfo.FilePath)
	}
}

// The row vanishing mid-repack (an out-of-band account purge — the API delete is
// refused while compression holds the claim) must not leave the recreated
// object behind: it is invisible to the byte quota and to a purge that works
// from rows. Same-extension repack is the sharp case,
// because the ordinary failure cleanup deliberately skips compressedPath there.
func TestRunCompressionRowDeletedMidRepackCleansRecreatedObject(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{ext: "xz"}, testLogger())
	svc.SetDecompressors(&fakeDecompressor{})

	raw := "raw image payload"
	xzCmd := cmd
	xzCmd.FileName = "official.img.xz"
	seedRawUpload(t, svc, xzCmd, "COMP:"+raw)
	sourcePath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)
	origPath := fmt.Sprintf("%s/%s.orig.xz", cmd.AuthContext.AccountID, cmd.ID)

	// The purge deletes the row out from under the running job.
	delete(repo.images, cmd.ID)

	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, sourcePath, "official.img.xz", int64(len("COMP:"+raw)), "")

	if _, ok := store.files[sourcePath]; ok {
		t.Error("recreated output must be removed when the row is gone, even on a same-path repack")
	}
	if _, ok := store.files[origPath]; ok {
		t.Error("preserved .orig copy must be removed when the row is gone")
	}
}

func TestDeleteRemovesStoredFiles(t *testing.T) {
	svc, _, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	content := "raw image payload"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, rawPath, cmd.FileName, int64(len(content)), "")
	compressedPath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)
	if _, ok := store.files[compressedPath]; !ok {
		t.Fatal("compressed file missing after compression")
	}

	if err := svc.Delete(context.Background(), cmd.AuthContext.AccountID, cmd.ID); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, ok := store.files[compressedPath]; ok {
		t.Error("compressed file must be removed on image delete")
	}
	if _, ok := store.files[rawPath]; ok {
		t.Error("retained original must be removed on image delete")
	}
}

func TestUploadReplaceCleansCompressedAndSource(t *testing.T) {
	svc, _, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	content := "raw image payload"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, rawPath, cmd.FileName, int64(len(content)), "")
	compressedPath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)

	// Replace with a fresh .iso upload: both old files must be dropped.
	newCmd := cmd
	newCmd.FileName = "other.iso"
	newContent := "brand new image"
	newCmd.SizeBytes = int64(len(newContent))
	img, err := svc.UploadFile(context.Background(), newCmd, strings.NewReader(newContent))
	if err != nil {
		t.Fatalf("replacement upload failed: %v", err)
	}
	if _, ok := store.files[compressedPath]; ok {
		t.Error("old compressed file must be removed on re-upload")
	}
	if _, ok := store.files[rawPath]; ok {
		t.Error("old retained original must be removed on re-upload")
	}
	if img.FileStorageInfo.SourceFilePath != "" {
		t.Error("fresh upload must not reference a source file")
	}
}

func TestUploadFileRepoFailureCleansUpNewFile(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	repo.updateErr = errors.New("db down")

	content := "0123456789"
	cmd.SizeBytes = int64(len(content))
	_, err := svc.UploadFile(context.Background(), cmd, strings.NewReader(content))
	if err == nil {
		t.Fatal("expected upload to fail")
	}

	newPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	if _, ok := store.files[newPath]; ok {
		t.Error("stored file was not cleaned up after repository failure")
	}
}

func TestCreateRejectsAtAccountCap(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	cfg := &config.Config{}
	cfg.Image.MaxPerAccount = 1
	svc := NewService(repo, store, cfg)

	accountID := uuid.New()
	seeded := &image.Image{ID: uuid.New(), AccountID: accountID}
	if err := repo.Create(context.Background(), seeded); err != nil {
		t.Fatalf("failed to seed image: %v", err)
	}

	_, err := svc.Create(context.Background(), image_create.ImageCreateCommand{
		ID:          uuid.New(),
		Title:       "second",
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	})
	if !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("expected ErrImageLimitReached, got %v", err)
	}

	// A different account is unaffected by this account's cap.
	if _, err := svc.Create(context.Background(), image_create.ImageCreateCommand{
		ID:          uuid.New(),
		Title:       "other tenant",
		AuthContext: commands.NewAuthContext(uuid.New(), uuid.New()),
	}); err != nil {
		t.Fatalf("other account must not be capped: %v", err)
	}
}

func TestRunCompressionReplacesOriginalWhenConfigured(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	cfg := &config.Config{}
	cfg.Image.ReplaceOriginalOnCompress = true
	svc := NewService(repo, store, cfg)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	accountID := uuid.New()
	imageID := uuid.New()
	if err := repo.Create(context.Background(), &image.Image{ID: imageID, AccountID: accountID}); err != nil {
		t.Fatalf("failed to seed image: %v", err)
	}
	cmd := image_upload.ImageUploadCommand{
		ID:          imageID,
		FileName:    "ubuntu-24.04.img",
		ContentType: "application/octet-stream",
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}
	content := "raw image payload"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", accountID, imageID)

	svc.runCompression(context.Background(), accountID, imageID, rawPath, cmd.FileName, int64(len(content)), "")

	img, err := repo.GetByID(context.Background(), accountID, imageID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	compressedPath := fmt.Sprintf("%s/%s.xz", accountID, imageID)
	if img.FileStorageInfo == nil || img.FileStorageInfo.FilePath != compressedPath {
		t.Fatalf("image does not point at compressed file: %+v", img.FileStorageInfo)
	}
	// The original must be gone and unreferenced.
	if _, ok := store.files[rawPath]; ok {
		t.Error("original file must be deleted when replace_original_on_compress is on")
	}
	if img.FileStorageInfo.SourceFilePath != "" || img.FileStorageInfo.SourceSizeBytes != 0 {
		t.Errorf("source must not be recorded, got %q/%d", img.FileStorageInfo.SourceFilePath, img.FileStorageInfo.SourceSizeBytes)
	}
	// Without a retained source the completed-progress marker must still make
	// our own output final (no re-compression loop).
	_, err = svc.StartCompression(context.Background(), image_compress.ImageCompressCommand{
		ID:          imageID,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	})
	if !errors.Is(err, app_errors.ErrInvalidImageFile) {
		t.Fatalf("expected deploy-format rejection, got %v", err)
	}
}

// A storage write error mid-compression must fail the job promptly, not
// deadlock on the compressor goroutine (regression: the output pipe was never
// closed on the store-error path, so the compressor blocked forever writing to
// a pipe nobody drained).
func TestRunCompressionStorageErrorDoesNotDeadlock(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	content := "raw image payload that the compressor will try to stream"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	// The store rejects the write without draining its reader — exactly what a
	// full disk does, and what wedged the compressor before the fix.
	store.storeErr = errors.New("disk full")

	done := make(chan struct{})
	go func() {
		svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, rawPath, cmd.FileName, int64(len(content)), "")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runCompression deadlocked on a storage write error")
	}

	img, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	if img.CompressionStatus != image.CompressionFailed {
		t.Errorf("compression status = %q, want %q", img.CompressionStatus, image.CompressionFailed)
	}
	// The original must survive a failed compression.
	if string(store.files[rawPath]) != content {
		t.Error("original file must be kept after a failed compression")
	}
}

func TestCaptureRoundTrip(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	cfg := &config.Config{}
	cfg.JWT.SecretKey = "test-secret"
	cfg.Server.AdvertisementURL = "https://platform.test"
	svc := NewService(repo, store, cfg)
	ctx := context.Background()
	accountID := uuid.New()

	img, err := svc.CreateCaptureImage(ctx, accountID, "golden", true, uuid.New(), "node-1")
	if err != nil {
		t.Fatalf("CreateCaptureImage: %v", err)
	}
	if img.CompressionStatus != image.CaptureInProgress {
		t.Fatalf("status = %q, want %q", img.CompressionStatus, image.CaptureInProgress)
	}
	if img.Kind != image.KindGolden {
		t.Errorf("kind = %q, want golden", img.Kind)
	}
	if img.SourceNodeName != "node-1" || img.SourceNodeID == nil {
		t.Errorf("source node not recorded: %q %v", img.SourceNodeName, img.SourceNodeID)
	}

	url, err := svc.CaptureUploadURL(ctx, accountID, img.ID, time.Hour)
	if err != nil {
		t.Fatalf("CaptureUploadURL: %v", err)
	}
	if !strings.Contains(url, "/capture/"+img.ID.String()) || !strings.Contains(url, "sig=") {
		t.Fatalf("unexpected upload URL: %s", url)
	}
	u, _ := neturl.Parse(url)
	exp, sig := u.Query().Get("exp"), u.Query().Get("sig")

	payload := "fake-zstd-image-bytes"
	final, err := svc.FinalizeCapture(ctx, img.ID, exp, sig, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("FinalizeCapture: %v", err)
	}
	if final.CompressionStatus != "" || final.CompressionProgress != 100 {
		t.Errorf("finalized status/progress = %q/%d, want \"\"/100", final.CompressionStatus, final.CompressionProgress)
	}
	if final.FileStorageInfo == nil || final.FileStorageInfo.SizeBytes != int64(len(payload)) {
		t.Fatalf("bad file storage info: %+v", final.FileStorageInfo)
	}
	wantSum := sha256.Sum256([]byte(payload))
	if final.Checksum != hex.EncodeToString(wantSum[:]) {
		t.Errorf("checksum mismatch")
	}

	// A tampered signature is refused.
	if _, err := svc.FinalizeCapture(ctx, img.ID, exp, sig+"00", strings.NewReader(payload)); err == nil {
		t.Error("expected FinalizeCapture to reject a bad signature")
	}

	// A REPLAY with the still-valid signature is refused too, and refused
	// without touching the stored bytes: the capability is stateless and lives
	// for hours, so single-use is a property of the record's state, not of the
	// signature.
	if _, err := svc.FinalizeCapture(ctx, img.ID, exp, sig, strings.NewReader("attacker-bytes")); err == nil {
		t.Fatal("expected FinalizeCapture to refuse a replayed capability on a finalized image")
	}
	after, err := repo.GetByIDGlobal(ctx, img.ID)
	if err != nil {
		t.Fatalf("failed to reload image: %v", err)
	}
	if after.Checksum != hex.EncodeToString(wantSum[:]) {
		t.Error("a replayed capture rewrote the finalized image's checksum")
	}
}

// capturedImageWithCapability seeds a capture record and mints its signed
// capability, the state every capture-stream test starts from.
func capturedImageWithCapability(t *testing.T, svc *Service, accountID uuid.UUID) (*image.Image, string, string) {
	t.Helper()
	ctx := context.Background()
	img, err := svc.CreateCaptureImage(ctx, accountID, "golden", false, uuid.New(), "node-1")
	if err != nil {
		t.Fatalf("CreateCaptureImage: %v", err)
	}
	url, err := svc.CaptureUploadURL(ctx, accountID, img.ID, time.Hour)
	if err != nil {
		t.Fatalf("CaptureUploadURL: %v", err)
	}
	u, _ := neturl.Parse(url)
	return img, u.Query().Get("exp"), u.Query().Get("sig")
}

// While the legitimate capture stream is in flight, a second PUT with the SAME
// valid capability must lose — atomically, not through a status check the two
// requests both pass. Without the claim, both streams wrote the same object key
// and the last finalize stamped its checksum over the other's bytes.
func TestCaptureStreamAdmitsExactlyOneConcurrentPUT(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	cfg := &config.Config{}
	cfg.JWT.SecretKey = "test-secret"
	cfg.Server.AdvertisementURL = "https://platform.test"
	svc := NewService(repo, store, cfg)
	ctx := context.Background()
	accountID := uuid.New()
	img, exp, sig := capturedImageWithCapability(t, svc, accountID)

	// The first stream parks mid-body on a pipe, exactly as a device pausing
	// between blocks would.
	pr, pw := io.Pipe()
	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.FinalizeCapture(ctx, img.ID, exp, sig, pr)
		firstDone <- err
	}()

	// Wait until the first stream owns the claim.
	deadline := time.After(5 * time.Second)
	for {
		got, err := repo.GetByIDGlobal(ctx, img.ID)
		if err != nil {
			t.Fatalf("failed to reload image: %v", err)
		}
		if got.CompressionStatus == image.CaptureUploadInProgress {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the capture stream never took its claim")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// The replayed PUT arrives while the stream is parked. Same valid
	// capability; it must lose at the claim, before touching storage.
	if _, err := svc.FinalizeCapture(ctx, img.ID, exp, sig, strings.NewReader("attacker-bytes")); err == nil {
		t.Fatal("a concurrent PUT with a replayed capability was admitted")
	}

	payload := "legitimate-capture-bytes"
	if _, err := pw.Write([]byte(payload)); err != nil {
		t.Fatalf("writing the capture body: %v", err)
	}
	_ = pw.Close()
	if err := <-firstDone; err != nil {
		t.Fatalf("the legitimate stream failed: %v", err)
	}

	final, err := repo.GetByIDGlobal(ctx, img.ID)
	if err != nil {
		t.Fatalf("failed to reload image: %v", err)
	}
	wantSum := sha256.Sum256([]byte(payload))
	if final.Checksum != hex.EncodeToString(wantSum[:]) {
		t.Error("the finalized checksum is not the legitimate stream's")
	}
	if final.CompressionStatus != "" {
		t.Errorf("finalized status = %q, want empty", final.CompressionStatus)
	}
}

// A capture whose agent vanishes mid-stream must settle back to CaptureFailed —
// on a DETACHED context, because the disconnect has already cancelled the
// request context, and a release that dies with it leaves the claim held until
// the next restart, refusing the retry the failure exists to allow. The same
// property the upload claim's release test asserts.
func TestFailedCaptureSettlesItsClaimOnACancelledRequest(t *testing.T) {
	repo := newFakeImageRepo()
	store := newFakeStorage()
	cfg := &config.Config{}
	cfg.JWT.SecretKey = "test-secret"
	cfg.Server.AdvertisementURL = "https://platform.test"
	svc := NewService(repo, store, cfg)
	accountID := uuid.New()
	img, exp, sig := capturedImageWithCapability(t, svc, accountID)

	ctx, cancel := context.WithCancel(context.Background())
	store.onStore = func(string) {
		store.onStore = nil
		cancel()
	}
	defer cancel()

	if _, err := svc.FinalizeCapture(ctx, img.ID, exp, sig, strings.NewReader("bytes")); err == nil {
		t.Fatal("expected the cancelled capture to fail")
	}

	after, err := repo.GetByIDGlobal(context.Background(), img.ID)
	if err != nil {
		t.Fatalf("failed to reload image: %v", err)
	}
	if after.CompressionStatus != image.CaptureFailed {
		t.Fatalf("status = %q, want %q — the record is unretryable until the backend restarts",
			after.CompressionStatus, image.CaptureFailed)
	}
}

// A second capture started while the first is still in flight must be refused.
// This test used to pass against a fake that counted every row
// regardless of status, which is NOT what the SQL did: the real count query
// excluded `capturing` rows, so the second capture went through and a backup
// playbook over N nodes produced N images against a cap of 1.
func TestCaptureRespectsPerAccountCap(t *testing.T) {
	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxPerAccount = 1
	cfg.Image.CaptureInFlightWindowMinutes = 60
	svc := NewService(repo, newFakeStorage(), cfg)
	ctx := context.Background()
	accountID := uuid.New()

	if _, err := svc.CreateCaptureImage(ctx, accountID, "one", false, uuid.New(), "node-1"); err != nil {
		t.Fatalf("first capture create: %v", err)
	}
	if _, err := svc.CreateCaptureImage(ctx, accountID, "two", false, uuid.New(), "node-1"); err == nil {
		t.Error("expected the second capture to hit the per-account cap")
	}
}

// An in-flight capture holds its slot only for the window. Past it the record
// stops counting, so one abandoned capture cannot brick image creation — the
// deliberate design the original count query protected, and the reason the fix
// is a window rather than "count in-progress rows".
func TestAbandonedCaptureAgesOutOfTheCap(t *testing.T) {
	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxPerAccount = 1
	cfg.Image.CaptureInFlightWindowMinutes = 60
	svc := NewService(repo, newFakeStorage(), cfg)
	ctx := context.Background()
	accountID := uuid.New()

	stale := image.NewCaptureImage(uuid.New(), accountID, "abandoned", false, uuid.New(), "node-1")
	stale.CreatedAt = time.Now().Add(-2 * time.Hour)
	if err := repo.Create(ctx, stale); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := svc.CreateCaptureImage(ctx, accountID, "fresh", false, uuid.New(), "node-2"); err != nil {
		t.Fatalf("a capture abandoned two hours ago still held the slot: %v", err)
	}
}

// A failed capture never counts, whatever the window says: nothing is going to
// arrive and it holds no stored bytes.
func TestFailedCaptureDoesNotHoldASlot(t *testing.T) {
	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxPerAccount = 1
	cfg.Image.CaptureInFlightWindowMinutes = 60
	svc := NewService(repo, newFakeStorage(), cfg)
	ctx := context.Background()
	accountID := uuid.New()

	failed := image.NewCaptureImage(uuid.New(), accountID, "failed", false, uuid.New(), "node-1")
	failed.CompressionStatus = image.CaptureFailed
	if err := repo.Create(ctx, failed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := svc.CreateCaptureImage(ctx, accountID, "retry", false, uuid.New(), "node-2"); err != nil {
		t.Fatalf("a failed capture held the slot: %v", err)
	}
}

// The window is a knob, and 0 restores exactly the behaviour that shipped
// before it existed. Asserted so a future reader can tell the default from the
// mechanism: with no window, concurrent captures do not see each other.
func TestZeroWindowCountsNoInFlightCapture(t *testing.T) {
	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxPerAccount = 1
	cfg.Image.CaptureInFlightWindowMinutes = 0
	svc := NewService(repo, newFakeStorage(), cfg)
	ctx := context.Background()
	accountID := uuid.New()

	if _, err := svc.CreateCaptureImage(ctx, accountID, "one", false, uuid.New(), "node-1"); err != nil {
		t.Fatalf("first capture create: %v", err)
	}
	if _, err := svc.CreateCaptureImage(ctx, accountID, "two", false, uuid.New(), "node-2"); err != nil {
		t.Fatalf("with no window, a concurrent capture is deliberately allowed: %v", err)
	}
}

// The finalize backstop: a capture that outlived the window while other images
// were created in the meantime must not become image number cap+1. No lock can
// span a multi-GB stream, so this is the one place the create-time check cannot
// cover.
func TestFinalizeCaptureRefusesOverTheCap(t *testing.T) {
	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxPerAccount = 1
	cfg.Image.CaptureInFlightWindowMinutes = 60
	cfg.JWT.SecretKey = "test-secret-key-for-capture-uploads"
	svc := NewService(repo, newFakeStorage(), cfg)
	ctx := context.Background()
	accountID := uuid.New()

	// A capture started long enough ago that it no longer counts...
	capturing := image.NewCaptureImage(uuid.New(), accountID, "long", false, uuid.New(), "node-1")
	capturing.CreatedAt = time.Now().Add(-2 * time.Hour)
	if err := repo.Create(ctx, capturing); err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	// ...and a finished image that took the account's only slot while it ran.
	if err := repo.Create(ctx, storedImage(accountID, 1024, 0)); err != nil {
		t.Fatalf("seed stored image: %v", err)
	}

	rawURL, err := svc.CaptureUploadURL(ctx, accountID, capturing.ID, time.Hour)
	if err != nil {
		t.Fatalf("CaptureUploadURL: %v", err)
	}
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse signed URL: %v", err)
	}
	exp := parsed.Query().Get("exp")
	sig := parsed.Query().Get("sig")

	_, err = svc.FinalizeCapture(ctx, capturing.ID, exp, sig, strings.NewReader("bytes"))
	if !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("FinalizeCapture error = %v, want ErrImageLimitReached", err)
	}
	// It is failed rather than left capturing, so it stops holding a slot and
	// the operator sees why.
	stored, err := repo.GetByIDGlobal(ctx, capturing.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.CompressionStatus != image.CaptureFailed {
		t.Errorf("status after a refused finalize = %q, want %q", stored.CompressionStatus, image.CaptureFailed)
	}
}

// storedImage seeds a record that occupies bytes, the way a finished upload
// leaves one.
func storedImage(accountID uuid.UUID, size, sourceSize int64) *image.Image {
	img := &image.Image{ID: uuid.New(), AccountID: accountID}
	img.FileStorageInfo = &image.FileStorageInfo{SizeBytes: size, SourceSizeBytes: sourceSize}
	return img
}

// A count cap and a byte ceiling answer different questions, which is the whole
// reason both exist: three images of 5 GiB is 15 GiB against a 10 GiB account,
// so the count says yes while the bytes say no.
func TestRemainingUploadBytesIsTheTighterOfAccountAndInstance(t *testing.T) {
	const gib = int64(1) << 30
	ctx := context.Background()
	accountID, otherAccount := uuid.New(), uuid.New()

	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 10 * gib
	svc := NewService(repo, newFakeStorage(), cfg)

	if err := repo.Create(ctx, storedImage(accountID, 4*gib, 0)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	remaining, err := svc.RemainingUploadBytes(ctx, accountID, nil)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if remaining != 6*gib {
		t.Errorf("remaining = %d GiB, want 6", remaining/gib)
	}

	// Another account's usage is not this account's problem...
	if err := repo.Create(ctx, storedImage(otherAccount, 9*gib, 0)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if remaining, err = svc.RemainingUploadBytes(ctx, accountID, nil); err != nil || remaining != 6*gib {
		t.Errorf("remaining = %d, err = %v; another account's images must not consume this one's quota", remaining, err)
	}

	// ...until the INSTANCE ceiling binds, which is the point of having one:
	// self-service signup means N accounts is N times the per-account quota.
	cfg.Image.MaxBytesInstance = 14 * gib
	if remaining, err = svc.RemainingUploadBytes(ctx, accountID, nil); err != nil || remaining != 1*gib {
		t.Errorf("remaining = %d, want 1 GiB (14 total - 13 used), err = %v", remaining, err)
	}
}

// The trap: an upload into an EXISTING record replaces its file. Counting the
// bytes it is about to drop refuses a same-size swap on a full account, which
// reads as "the quota is broken" rather than as a quota.
func TestRemainingUploadBytesExcludesTheRecordBeingReplaced(t *testing.T) {
	const gib = int64(1) << 30
	ctx := context.Background()
	accountID := uuid.New()

	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 10 * gib
	cfg.Image.MaxBytesInstance = 10 * gib
	svc := NewService(repo, newFakeStorage(), cfg)

	existing := storedImage(accountID, 10*gib, 0)
	if err := repo.Create(ctx, existing); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if remaining, err := svc.RemainingUploadBytes(ctx, accountID, nil); err != nil || remaining != 0 {
		t.Errorf("remaining = %d, want 0 for a full account, err = %v", remaining, err)
	}
	// Both ceilings must exclude it, not just the account one — the instance
	// total contains the same record.
	remaining, err := svc.RemainingUploadBytes(ctx, accountID, &existing.ID)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if remaining != 10*gib {
		t.Errorf("remaining = %d GiB, want the full 10: replacing a file frees what it occupied", remaining/gib)
	}
}

// A retained pre-compression original is real stored data. An install that
// keeps it (IMAGE_COMPRESS_REPLACE_ORIGINAL=false) pays roughly twice for the
// same image — counting only the current file would under-report exactly
// where space is tightest.
func TestRemainingUploadBytesCountsARetainedOriginal(t *testing.T) {
	const gib = int64(1) << 30
	ctx := context.Background()
	accountID := uuid.New()

	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 10 * gib
	svc := NewService(repo, newFakeStorage(), cfg)

	if err := repo.Create(ctx, storedImage(accountID, 2*gib, 5*gib)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	remaining, err := svc.RemainingUploadBytes(ctx, accountID, nil)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if remaining != 3*gib {
		t.Errorf("remaining = %d GiB, want 3 (10 - 2 compressed - 5 retained original)", remaining/gib)
	}
}

// No ceiling configured must not read as "no room". On-prem installs set
// neither, and returning 0 there would refuse every upload.
func TestNoByteCeilingIsNotAFullQuota(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()
	repo := newFakeImageRepo()
	svc := NewService(repo, newFakeStorage(), &config.Config{})

	if err := repo.Create(ctx, storedImage(accountID, 1<<40, 0)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	remaining, err := svc.RemainingUploadBytes(ctx, accountID, nil)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if remaining != services.NoByteCeiling {
		t.Fatalf("remaining = %d, want the NoByteCeiling sentinel", remaining)
	}
	// And creating an image on an unlimited install is unaffected.
	if _, err := svc.Create(ctx, image_create.ImageCreateCommand{
		ID: uuid.New(), Title: "unbounded",
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}); err != nil {
		t.Fatalf("create on an install with no ceiling: %v", err)
	}
}

// A full account is refused before a record is created, so nobody gets an image
// row they can never fill.
func TestCreateRefusesWhenTheByteQuotaIsFull(t *testing.T) {
	const gib = int64(1) << 30
	ctx := context.Background()
	accountID := uuid.New()

	repo := newFakeImageRepo()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 10 * gib
	svc := NewService(repo, newFakeStorage(), cfg)

	if err := repo.Create(ctx, storedImage(accountID, 10*gib, 0)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := svc.Create(ctx, image_create.ImageCreateCommand{
		ID: uuid.New(), Title: "no room",
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	})
	if !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("expected ErrImageLimitReached, got %v", err)
	}
	// A capture is the same quota, reached from the other side.
	if _, err := svc.CreateCaptureImage(ctx, accountID, "no room", false, uuid.New(), "node-1"); !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("expected a capture to hit the same ceiling, got %v", err)
	}
}

// gatedCompressor blocks every Compress call until release closes, recording
// how many ran at once — the observable the concurrency cap is about.
type gatedCompressor struct {
	mu      sync.Mutex
	active  int
	maxSeen int
	started chan struct{}
	release chan struct{}
}

func (g *gatedCompressor) Compress(ctx context.Context, dst io.Writer, src io.Reader) error {
	g.mu.Lock()
	g.active++
	if g.active > g.maxSeen {
		g.maxSeen = g.active
	}
	g.mu.Unlock()
	g.started <- struct{}{}
	<-g.release
	defer func() {
		g.mu.Lock()
		g.active--
		g.mu.Unlock()
	}()
	_, err := io.Copy(dst, src)
	return err
}

func (g *gatedCompressor) Extension() string   { return "xz" }
func (g *gatedCompressor) ContentType() string { return "application/x-xz" }

// Compression jobs are all-core xz/zstd pipelines; unbounded, tenants could
// stack them beside Postgres and the gRPC control plane until heartbeats
// starve. The semaphore must hold extra jobs — already claimed,
// so queued rather than refused — until a slot frees, and let them all finish.
func TestRunCompressionBoundsConcurrentJobs(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	gc := &gatedCompressor{
		started: make(chan struct{}, config.DefaultImageMaxConcurrentCompressions+2),
		release: make(chan struct{}),
	}
	svc.SetCompressor(gc, testLogger())

	const jobs = config.DefaultImageMaxConcurrentCompressions + 2
	content := "raw image payload"
	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		imgID := uuid.New()
		if err := repo.Create(context.Background(), &image.Image{ID: imgID, AccountID: cmd.AuthContext.AccountID, Title: "img"}); err != nil {
			t.Fatalf("seeding image: %v", err)
		}
		rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, imgID)
		store.mu.Lock()
		store.files[rawPath] = []byte(content)
		store.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.runCompression(context.Background(), cmd.AuthContext.AccountID, imgID, rawPath, "img.img", int64(len(content)), "")
		}()
	}

	for i := 0; i < config.DefaultImageMaxConcurrentCompressions; i++ {
		<-gc.started
	}
	select {
	case <-gc.started:
		t.Fatal("a compression job started while every slot was held")
	case <-time.After(100 * time.Millisecond):
	}

	close(gc.release)
	wg.Wait()

	if gc.maxSeen != config.DefaultImageMaxConcurrentCompressions {
		t.Errorf("max concurrent compressions = %d, want %d", gc.maxSeen, config.DefaultImageMaxConcurrentCompressions)
	}
}

// UploadFile checks the compression status when it starts and then
// streams for minutes; a compress claimed inside that window read the record as
// it was before the upload, deleted what it believed was the stale object — the
// file the upload had just written — and republished the PREVIOUS image's bytes
// under the same record, which then advertised a checksum and a size for one
// image while serving another.
//
// The two halves are tested separately because they are two different
// mechanisms: the upload must lose the write while a job holds the record, and
// the swap must refuse to publish over a record that no longer describes what it
// compressed.

func TestCompressionIsRefusedWhileAnUploadIsStreaming(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	original := "the original image"
	seedRawUpload(t, svc, cmd, original)

	// The compress attempt must land INSIDE the upload — after its opening check
	// and while the bytes are going in. Seeding a state before the call would
	// only exercise the opening check, which has always been there, and a test
	// that passes against the unfixed code is worse than no test. (Verified by
	// putting the defect back: with the claim removed, the compress below is
	// admitted and this fails.)
	var compressErr error
	var compressAttempted bool
	store.onStore = func(string) {
		store.onStore = nil
		compressAttempted = true
		_, compressErr = svc.StartCompression(context.Background(), image_compress.ImageCompressCommand{
			ID:          cmd.ID,
			AuthContext: cmd.AuthContext,
		})
	}

	replacement := "a completely different image"
	upload := cmd
	upload.SizeBytes = int64(len(replacement))
	if _, err := svc.UploadFile(context.Background(), upload, strings.NewReader(replacement)); err != nil {
		t.Fatalf("the upload itself must succeed: %v", err)
	}

	if !compressAttempted {
		t.Fatal("the compress was never attempted; the test proves nothing")
	}
	if !errors.Is(compressErr, app_errors.ErrImageCompressionInProgress) {
		t.Fatalf("compress during an upload = %v, want ErrImageCompressionInProgress", compressErr)
	}

	// The upload won, so the record must describe the replacement — and the
	// claim it held must be gone, or the image is stuck busy forever.
	after, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to reload image: %v", err)
	}
	if after.CompressionStatus != "" {
		t.Errorf("record left busy after the upload: status = %q", after.CompressionStatus)
	}
	if after.FileStorageInfo == nil || after.FileStorageInfo.SizeBytes != int64(len(replacement)) {
		t.Errorf("record does not describe the uploaded file: %+v", after.FileStorageInfo)
	}
	sum := sha256.Sum256([]byte(replacement))
	if after.Checksum != hex.EncodeToString(sum[:]) {
		t.Error("checksum does not cover the uploaded file")
	}
}

func TestFailedUploadReleasesItsClaim(t *testing.T) {
	svc, repo, _, cmd := setupService(t)

	// A size mismatch is the ordinary failure: the client declared one length
	// and the stream carried another. The record must come back available, or
	// the first truncated upload leaves the image permanently unable to be
	// re-uploaded or compressed — a worse outcome than the failure itself.
	upload := cmd
	upload.SizeBytes = 999
	if _, err := svc.UploadFile(context.Background(), upload, strings.NewReader("short")); err == nil {
		t.Fatal("expected the size mismatch to fail the upload")
	}

	after, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to reload image: %v", err)
	}
	if after.CompressionStatus != "" {
		t.Fatalf("failed upload left the record busy: status = %q", after.CompressionStatus)
	}
}

func TestCompressionRefusesToPublishOverAReplacedImage(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	content := "raw image payload"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	compressedPath := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)

	before, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}

	// Run the job with the checksum of a DIFFERENT image than the record now
	// holds — which is precisely the state a compress claimed mid-upload ends up
	// in, and the only part of that race that decides whether data is lost.
	stale := "0000000000000000000000000000000000000000000000000000000000000000"
	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, rawPath, cmd.FileName, int64(len(content)), stale)

	after, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to reload image: %v", err)
	}
	if after.FileStorageInfo == nil || after.FileStorageInfo.FilePath != rawPath {
		t.Fatalf("the record was repointed at compressed output of an image it no longer holds: %+v", after.FileStorageInfo)
	}
	if after.Checksum != before.Checksum {
		t.Errorf("checksum was overwritten: %q, want %q", after.Checksum, before.Checksum)
	}
	// Not "failed": the record belongs to whoever replaced it, and reporting a
	// compression failure on their successful upload is a lie about their image.
	if after.CompressionStatus == image.CompressionFailed {
		t.Errorf("the replacing upload's record was marked failed")
	}
	// The file the record points at must survive — deleting it as "the stale
	// source" is the destructive half of the original defect.
	if got := string(store.files[rawPath]); got != content {
		t.Errorf("the record's file was destroyed: %q, want %q", got, content)
	}
	// And the abandoned output must not be left behind: it is unreferenced, so
	// nothing would ever name it again.
	if _, present := store.files[compressedPath]; present {
		t.Errorf("abandoned compression output was left in storage at %s", compressedPath)
	}
}

// A same-extension repack stages the untouched original under a
// derived ".orig" name so the output can overwrite the source key. A restart
// between that copy and the swap leaves a full copy of the customer's image
// behind under a name that appears on no row — invisible to the per-account byte
// quota and to an out-of-band account purge, both of which work from rows. The
// customer can delete the image and still be paying for it.
func TestSweepRemovesArtifactsOrphanedByARestart(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	content := "raw image payload"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	origPath := repackOriginalPath(rawPath)
	partialOutput := fmt.Sprintf("%s/%s.xz", cmd.AuthContext.AccountID, cmd.ID)

	// The state a killed job leaves: the record still says in_progress, the
	// staged original and a half-written output are both in storage, and no row
	// mentions either.
	img, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	img.CompressionStatus = image.CompressionInProgress
	if err := repo.Update(context.Background(), img); err != nil {
		t.Fatalf("failed to seed the stale status: %v", err)
	}
	store.files[origPath] = []byte(content)
	store.files[partialOutput] = []byte("XZ:partial")

	if err := svc.SweepStaleCompressionArtifacts(context.Background()); err != nil {
		t.Fatalf("sweep failed: %v", err)
	}

	if _, present := store.files[origPath]; present {
		t.Errorf("the staged original was left behind at %s", origPath)
	}
	if _, present := store.files[partialOutput]; present {
		t.Errorf("the half-written output was left behind at %s", partialOutput)
	}
	// The record's own file is not an artifact.
	if got := string(store.files[rawPath]); got != content {
		t.Fatalf("the sweep destroyed the record's file: %q", got)
	}
}

// The sweep deletes objects at startup, so the property that matters most is
// what it REFUSES to touch: a wrong delete costs the customer their image, while
// a missed orphan only costs disk.
func TestSweepNeverDeletesAReferencedFile(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.SetCompressor(&fakeCompressor{}, testLogger())

	// An image already in the deploy format with its pre-compression original
	// retained — the ordinary state of a compressed image on an install that
	// keeps originals. Both paths are referenced by the row.
	content := "raw image payload"
	seedRawUpload(t, svc, cmd, content)
	rawPath := fmt.Sprintf("%s/%s.img", cmd.AuthContext.AccountID, cmd.ID)
	svc.runCompression(context.Background(), cmd.AuthContext.AccountID, cmd.ID, rawPath, cmd.FileName, int64(len(content)), "")

	img, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	if img.FileStorageInfo == nil || img.FileStorageInfo.SourceFilePath == "" {
		t.Fatalf("expected a retained source to guard: %+v", img.FileStorageInfo)
	}
	current, source := img.FileStorageInfo.FilePath, img.FileStorageInfo.SourceFilePath

	// Now a second compression is claimed and the process dies. The derived
	// candidate for the output is the file the record is ALREADY serving.
	img.CompressionStatus = image.CompressionInProgress
	if err := repo.Update(context.Background(), img); err != nil {
		t.Fatalf("failed to seed the stale status: %v", err)
	}

	if err := svc.SweepStaleCompressionArtifacts(context.Background()); err != nil {
		t.Fatalf("sweep failed: %v", err)
	}

	if _, present := store.files[current]; !present {
		t.Errorf("the sweep deleted the image the record serves (%s)", current)
	}
	if _, present := store.files[source]; !present {
		t.Errorf("the sweep deleted the retained original (%s)", source)
	}
}

// The claim must be released even when the REQUEST context is already dead,
// which is the most likely way a multi-GB upload ends: a closed tab, a slept
// laptop, a proxy timeout. Releasing on the request context means `database/sql`
// refuses the UPDATE, the record stays `uploading` until the next backend
// restart, and every retry and every compress is refused in the meantime — a
// worse outcome than the failure that caused it.
func TestUploadReleasesItsClaimOnACancelledRequest(t *testing.T) {
	svc, repo, store, cmd := setupService(t)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel while the bytes are going in, exactly as a disconnect would.
	store.onStore = func(string) {
		store.onStore = nil
		cancel()
	}
	defer cancel()

	upload := cmd
	upload.SizeBytes = int64(len("payload"))
	// The upload may fail or succeed depending on the storage backend's own
	// ctx handling; what must hold either way is that the record is not left
	// claimed.
	_, _ = svc.UploadFile(ctx, upload, strings.NewReader("payload"))

	after, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to reload image: %v", err)
	}
	if after.CompressionStatus == image.UploadInProgress {
		t.Fatal("a cancelled upload left the record claimed; it is unusable until the backend restarts")
	}
}

// A capture landing on a record an upload now holds must not write over it: the
// write carries the WHOLE row, including compression_status, so it would release
// somebody else's claim mid-transfer and reopen the window the upload claim closes.
func TestCaptureFinalizeDoesNotClobberAnUploadClaim(t *testing.T) {
	_, repo, _, cmd := setupService(t)

	img, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to load image: %v", err)
	}
	img.CompressionStatus = image.UploadInProgress
	if err := repo.Update(context.Background(), img); err != nil {
		t.Fatalf("failed to seed the upload claim: %v", err)
	}

	// The capture's terminal write, with the record already claimed. Since the
	// atomic capture claim landed, the terminal write is conditional on OUR
	// claim (CaptureUploadInProgress), so a record held by an upload loses.
	finalized := *img
	finalized.CompressionStatus = ""
	finalized.CompressionProgress = 100
	won, err := repo.UpdateIfCaptureUploading(context.Background(), &finalized)
	if err != nil {
		t.Fatalf("UpdateIfCaptureUploading: %v", err)
	}
	if won {
		t.Fatal("a capture write was allowed over a live upload claim")
	}

	after, err := repo.GetByID(context.Background(), cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		t.Fatalf("failed to reload image: %v", err)
	}
	if after.CompressionStatus != image.UploadInProgress {
		t.Fatalf("the upload claim was released by another writer: status = %q", after.CompressionStatus)
	}
}
