package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/image_compress"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// expandingDecompressor turns a "COMP:<n>" source into n bytes, the way a
// small archive expands into a large image.
type expandingDecompressor struct{}

func (expandingDecompressor) Decompress(_ context.Context, dst io.Writer, src io.Reader) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	var n int
	if _, err := fmt.Sscanf(string(data), "COMP:%d", &n); err != nil {
		return err
	}
	_, err = io.Copy(dst, io.LimitReader(zeroReader{}, int64(n)))
	return err
}

func (expandingDecompressor) Extension() string { return "zst" }

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// seedStoredImage puts a record and its stored file in place, as an upload
// would have left them.
func seedStoredImage(t *testing.T, repo *fakeImageRepo, store *fakeStorage, accountID uuid.UUID, ext, content string) (uuid.UUID, string) {
	t.Helper()
	id := uuid.New()
	path := fmt.Sprintf("%s/%s%s", accountID, id, ext)
	img := &image.Image{ID: id, AccountID: accountID, Title: "img", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	img.FileStorageInfo = &image.FileStorageInfo{FilePath: path, FileName: "img" + ext, SizeBytes: int64(len(content))}
	if err := repo.Create(context.Background(), img); err != nil {
		t.Fatalf("seeding image: %v", err)
	}
	store.mu.Lock()
	store.files[path] = []byte(content)
	store.mu.Unlock()
	return id, path
}

// A compression's output is stored bytes like an upload's: a small archive
// that expands past what the account has left must fail without storing the
// result, and must leave no reservation behind.
func TestCompressionOutputIsBoundedByTheByteQuota(t *testing.T) {
	repo, store := newFakeImageRepo(), newFakeStorage()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 4096
	svc := NewService(repo, store, cfg)
	svc.SetCompressor(&fakeCompressor{ext: "xz"}, testLogger())
	svc.SetDecompressors(expandingDecompressor{})
	accountID := uuid.New()

	source := "COMP:1000000" // 12 bytes that expand to ~1 MB
	id, path := seedStoredImage(t, repo, store, accountID, ".zst", source)
	// The stream itself is cut at the quota: refusing at the swap alone would
	// still have written the whole expansion to storage first.
	var stored []string
	store.onStore = func(name string) { stored = append(stored, name) }
	svc.runCompression(context.Background(), accountID, id, path, "img.zst", int64(len(source)), "")
	if len(stored) != 0 {
		t.Errorf("the over-quota output reached storage (%v) before it was refused", stored)
	}

	img, _ := repo.GetByID(context.Background(), accountID, id)
	if img.CompressionStatus != image.CompressionFailed {
		t.Fatalf("status = %q, want failed: the output exceeds the quota", img.CompressionStatus)
	}
	if img.FileStorageInfo.FilePath != path || img.FileStorageInfo.SizeBytes != int64(len(source)) {
		t.Errorf("record moved to %+v; it must still describe the upload", img.FileStorageInfo)
	}
	if _, ok := store.files[fmt.Sprintf("%s/%s.xz", accountID, id)]; ok {
		t.Error("the over-quota output was left in storage")
	}
	if left, _ := svc.RemainingUploadBytes(context.Background(), accountID, nil); left != 4096-int64(len(source)) {
		t.Errorf("remaining = %d, want %d: the output's reservation must be released", left, 4096-len(source))
	}
}

// The retained original stays charged, so the output may take only what is
// left beside it; within that the compression succeeds and its reservation
// is released.
func TestCompressionWithinTheQuotaSucceedsAndReleasesItsReservation(t *testing.T) {
	repo, store := newFakeImageRepo(), newFakeStorage()
	cfg := &config.Config{}
	cfg.Image.MaxBytesPerAccount = 100
	svc := NewService(repo, store, cfg)
	svc.SetCompressor(&fakeCompressor{ext: "xz"}, testLogger())
	accountID := uuid.New()

	raw := strings.Repeat("r", 40)
	id, path := seedStoredImage(t, repo, store, accountID, ".img", raw)
	svc.runCompression(context.Background(), accountID, id, path, "img.img", int64(len(raw)), "")
	img, _ := repo.GetByID(context.Background(), accountID, id)
	if img.CompressionStatus != "" || img.FileStorageInfo.SourceFilePath != path {
		t.Fatalf("compression within the quota failed: status %q, info %+v", img.CompressionStatus, img.FileStorageInfo)
	}
	if left, _ := svc.RemainingUploadBytes(context.Background(), accountID, nil); left != 100-40-43 {
		t.Errorf("remaining = %d, want %d", left, 100-40-43)
	}

	// A second record with 100 bytes left beside the first: its 60-byte
	// original stays charged, so its 63-byte output does not fit.
	raw2 := strings.Repeat("r", 60)
	cfg.Image.MaxBytesPerAccount = 100 + 83
	id2, path2 := seedStoredImage(t, repo, store, accountID, ".img", raw2)
	svc.runCompression(context.Background(), accountID, id2, path2, "img.img", int64(len(raw2)), "")
	img2, _ := repo.GetByID(context.Background(), accountID, id2)
	if img2.CompressionStatus != image.CompressionFailed {
		t.Errorf("status = %q, want failed: the retained original leaves no room for the output", img2.CompressionStatus)
	}
}

// blockingCompressor runs until its context ends, as the codec processes do
// under exec.CommandContext, or until released.
type blockingCompressor struct {
	started chan uuid.UUID
	release chan struct{}
}

func (b *blockingCompressor) Compress(ctx context.Context, dst io.Writer, src io.Reader) error {
	b.started <- uuid.Nil
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.release:
	}
	_, err := io.Copy(dst, src)
	return err
}

func (b *blockingCompressor) Extension() string   { return "xz" }
func (b *blockingCompressor) ContentType() string { return "application/x-xz" }

// A job that runs past its deadline is stopped, marked failed, leaves no
// partial output and gives its slot back.
func TestCompressionStopsAtItsDeadline(t *testing.T) {
	repo, store := newFakeImageRepo(), newFakeStorage()
	cfg := &config.Config{}
	cfg.Image.MaxConcurrentCompressions = 1
	svc := NewService(repo, store, cfg)
	svc.compressionTimeout = 50 * time.Millisecond
	bc := &blockingCompressor{started: make(chan uuid.UUID, 2), release: make(chan struct{})}
	svc.SetCompressor(bc, testLogger())
	accountID := uuid.New()
	id, path := seedStoredImage(t, repo, store, accountID, ".img", "raw")

	done := make(chan struct{})
	go func() {
		svc.runCompression(context.Background(), accountID, id, path, "img.img", 3, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(bc.release)
		<-done
		t.Fatal("the job ran past its deadline")
	}
	img, _ := repo.GetByID(context.Background(), accountID, id)
	if img.CompressionStatus != image.CompressionFailed {
		t.Errorf("status = %q, want failed", img.CompressionStatus)
	}
	if _, ok := store.files[fmt.Sprintf("%s/%s.xz", accountID, id)]; ok {
		t.Error("partial output left behind")
	}
	// The slot is free again.
	release, err := svc.compressionSlots.acquire(context.Background(), accountID)
	if err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	release()
}

// Deleting an image whose compression is waiting for a slot — another
// account's job holds it — stops the queued job and deletes the image instead
// of refusing until the other job ends. Deleting the running one stops it too.
func TestDeletingAQueuedOrRunningCompressionStopsIt(t *testing.T) {
	repo, store := newFakeImageRepo(), newFakeStorage()
	repo.refuseBusyDelete = true
	cfg := &config.Config{}
	cfg.Image.MaxConcurrentCompressions = 1
	svc := NewService(repo, store, cfg)
	bc := &blockingCompressor{started: make(chan uuid.UUID, 4), release: make(chan struct{})}
	svc.SetCompressor(bc, testLogger())

	accountA, accountB := uuid.New(), uuid.New()
	idA, pathA := seedStoredImage(t, repo, store, accountA, ".img", "running")
	idB, pathB := seedStoredImage(t, repo, store, accountB, ".img", "queued")
	start := func(accountID, id uuid.UUID) {
		cmd := image_compress.ImageCompressCommand{ID: id, AuthContext: commands.NewAuthContext(accountID, uuid.New())}
		if _, err := svc.StartCompression(context.Background(), cmd); err != nil {
			t.Fatalf("start compression: %v", err)
		}
	}
	start(accountA, idA)
	<-bc.started
	start(accountB, idB)
	waitFor(t, "the second job to queue", func() bool {
		svc.compressionSlots.mu.Lock()
		defer svc.compressionSlots.mu.Unlock()
		return len(svc.compressionSlots.waiting) == 1
	})

	if err := svc.Delete(context.Background(), accountB, idB); err != nil {
		t.Fatalf("deleting the queued image: %v", err)
	}
	if _, err := repo.GetByID(context.Background(), accountB, idB); !errors.Is(err, app_errors.ErrImageNotFound) {
		t.Errorf("queued image still present: %v", err)
	}
	if _, ok := store.files[pathB]; ok {
		t.Error("queued image's file still stored")
	}

	if err := svc.Delete(context.Background(), accountA, idA); err != nil {
		t.Fatalf("deleting the running image: %v", err)
	}
	if _, err := repo.GetByID(context.Background(), accountA, idA); !errors.Is(err, app_errors.ErrImageNotFound) {
		t.Errorf("running image still present: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, p := range []string{pathA, fmt.Sprintf("%s/%s.xz", accountA, idA)} {
		if _, ok := store.files[p]; ok {
			t.Errorf("%s still stored after delete", p)
		}
	}
	select {
	case <-bc.started:
		t.Error("the queued job ran after its image was deleted")
	default:
	}
}

// One account queueing many jobs must not keep another account's job waiting
// behind all of them: a freed slot goes to the account not running anything
// that has waited longest.
func TestCompressionSlotsAreSharedFairlyAcrossAccounts(t *testing.T) {
	q := newCompressionQueue(1)
	accountA, accountB := uuid.New(), uuid.New()

	releaseFirst, err := q.acquire(context.Background(), accountA)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	enqueue := func(name string, accountID uuid.UUID) {
		before := func() int { q.mu.Lock(); defer q.mu.Unlock(); return len(q.waiting) }()
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := q.acquire(context.Background(), accountID)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			release()
		}()
		waitFor(t, name+" to queue", func() bool { q.mu.Lock(); defer q.mu.Unlock(); return len(q.waiting) == before+1 })
	}
	enqueue("A2", accountA)
	enqueue("A3", accountA)
	enqueue("B1", accountB)

	releaseFirst()
	wg.Wait()
	if got := strings.Join(order, ","); got != "B1,A2,A3" {
		t.Errorf("grant order = %s, want B1,A2,A3", got)
	}
}

// A cancelled wait leaves the queue, and a slot granted at the same moment is
// given back rather than lost.
func TestCompressionQueueWaitIsCancellable(t *testing.T) {
	q := newCompressionQueue(1)
	release, _ := q.acquire(context.Background(), uuid.New())
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := q.acquire(ctx, uuid.New())
		errc <- err
	}()
	waitFor(t, "the waiter to queue", func() bool { q.mu.Lock(); defer q.mu.Unlock(); return len(q.waiting) == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	release()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.total != 0 || len(q.waiting) != 0 {
		t.Errorf("queue not empty: total %d, waiting %d", q.total, len(q.waiting))
	}
}

// A transcode's source is no measure of its progress: a small archive is read
// at once and then expands for minutes. Reading the whole source must not
// report near-completion; the expanded bytes move the bar.
func TestTranscodeProgressDoesNotJumpWhenTheSourceIsReadAtOnce(t *testing.T) {
	var got []int
	now := time.Unix(0, 0)
	p := newCompressionProgress(5<<20, true, func(percent int) { got = append(got, percent) })
	p.now = func() time.Time { now = now.Add(time.Second); return now }

	p.addSource(5 << 20)
	if len(got) != 0 {
		t.Fatalf("reading the whole source reported %v before any output", got)
	}
	p.addExpanded(int(transcodeEstimateFloor / 2))
	if len(got) != 1 || got[0] != 50 {
		t.Fatalf("half the estimate expanded reported %v, want [50]", got)
	}
	p.addExpanded(int(transcodeEstimateFloor * 4))
	if last := got[len(got)-1]; last != maxLiveProgressTranscode {
		t.Errorf("past the estimate reported %d, want the %d ceiling", last, maxLiveProgressTranscode)
	}
}

// Progress updates are a database write and a frame to every viewer; however
// fast the bytes move, at most one goes out per interval.
func TestCompressionProgressIsPacedInTime(t *testing.T) {
	var got []int
	now := time.Unix(0, 0)
	p := newCompressionProgress(100, false, func(percent int) { got = append(got, percent) })
	p.now = func() time.Time { return now }

	for i := 0; i < 100; i++ {
		p.addSource(1)
	}
	if len(got) != 1 {
		t.Fatalf("100 reads in one instant sent %d updates (%v), want 1", len(got), got)
	}
	now = now.Add(progressMinInterval)
	p.addSource(0)
	if len(got) != 2 || got[1] != maxLiveProgressRaw {
		t.Errorf("after the interval: %v, want a second update at %d", got, maxLiveProgressRaw)
	}
}

// levelRecorder keeps the level and message of every record logged through it.
type levelRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *levelRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *levelRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
	return nil
}
func (r *levelRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *levelRecorder) WithGroup(string) slog.Handler      { return r }

// Deleting an image stops its running compression because the operator asked
// for it; that stop is not a failure and must not be logged as one.
func TestAnOperatorStoppedCompressionIsNotLoggedAsAnError(t *testing.T) {
	repo, store := newFakeImageRepo(), newFakeStorage()
	repo.refuseBusyDelete = true
	cfg := &config.Config{}
	cfg.Image.MaxConcurrentCompressions = 1
	svc := NewService(repo, store, cfg)
	bc := &blockingCompressor{started: make(chan uuid.UUID, 2), release: make(chan struct{})}
	rec := &levelRecorder{}
	svc.SetCompressor(bc, slog.New(rec))

	accountID := uuid.New()
	id, _ := seedStoredImage(t, repo, store, accountID, ".img", "running")
	cmd := image_compress.ImageCompressCommand{ID: id, AuthContext: commands.NewAuthContext(accountID, uuid.New())}
	if _, err := svc.StartCompression(context.Background(), cmd); err != nil {
		t.Fatalf("start compression: %v", err)
	}
	<-bc.started
	if err := svc.Delete(context.Background(), accountID, id); err != nil {
		t.Fatalf("deleting the running image: %v", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	stopped := false
	for _, r := range rec.records {
		if r.Level >= slog.LevelError {
			t.Errorf("an operator-requested stop logged at %s: %q", r.Level, r.Message)
		}
		if r.Message == "image compression stopped" {
			stopped = true
		}
	}
	if !stopped {
		t.Error("the stop was not logged at all")
	}
}
