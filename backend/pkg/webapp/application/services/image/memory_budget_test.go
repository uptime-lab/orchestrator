package image

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// Why these tests exist. The compression slots bounded JOBS
// and the xz limit bounded a PROCESS, but a transcode is two processes, so the
// ceiling a container had to survive was slots × 2 × limit with nothing
// enforcing it. The budget charges a job its whole weight before it starts.

func TestMemoryBudgetAdmitsUpToCapacityAndThenWaits(t *testing.T) {
	b := newMemoryBudget(512)
	if err := b.acquire(context.Background(), 256); err != nil {
		t.Fatal(err)
	}
	if err := b.acquire(context.Background(), 256); err != nil {
		t.Fatal(err)
	}
	admitted := make(chan struct{})
	go func() {
		_ = b.acquire(context.Background(), 256)
		close(admitted)
	}()
	select {
	case <-admitted:
		t.Fatal("a third 256 MiB job was admitted into a 512 MiB budget")
	case <-time.After(50 * time.Millisecond):
	}
	b.release(256)
	select {
	case <-admitted:
	case <-time.After(2 * time.Second):
		t.Fatal("the waiting job was not admitted after a release")
	}
	if got := b.inUse(); got != 512 {
		t.Fatalf("in use = %d, want 512", got)
	}
}

func TestMemoryBudgetWaitEndsWithTheContext(t *testing.T) {
	b := newMemoryBudget(256)
	if err := b.acquire(context.Background(), 256); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := b.acquire(ctx, 256); err == nil {
		t.Fatal("a cancelled wait was admitted")
	}
	if got := b.inUse(); got != 256 {
		t.Fatalf("a cancelled wait changed the budget: %d", got)
	}
}

func TestMemoryBudgetTakesAWholeTranscodeAtOnce(t *testing.T) {
	// Two transcodes into a budget of one: the second waits for the first to
	// finish entirely — it can never hold a decoder while waiting for an
	// encoder, which is the deadlock a per-process charge would allow.
	b := newMemoryBudget(512)
	if err := b.acquire(context.Background(), 512); err != nil {
		t.Fatal(err)
	}
	second := make(chan struct{})
	go func() { _ = b.acquire(context.Background(), 512); close(second) }()
	select {
	case <-second:
		t.Fatal("a second transcode was admitted beside the first")
	case <-time.After(50 * time.Millisecond):
	}
	b.release(512)
	<-second
}

func TestCompressionWeightChargesTwoProcessesForATranscode(t *testing.T) {
	cfg := &config.Config{}
	cfg.Image.XZMemLimitMB = 256
	svc := NewService(newFakeImageRepo(), newFakeStorage(), cfg)
	if got := svc.compressionWeightMB("acct/img.img"); got != 256 {
		t.Fatalf("raw source weight = %d, want 256", got)
	}
	for _, ext := range []string{".xz", ".zst"} {
		if got := svc.compressionWeightMB("acct/img.img" + ext); got != 512 {
			t.Fatalf("transcode weight for %s = %d, want 512", ext, got)
		}
	}
}

// End to end through runCompression: with two slots but a budget of exactly
// one transcode, two transcodes serialise while two plain compressions run
// side by side.
func TestRunCompressionHonoursTheMemoryBudget(t *testing.T) {
	svc, repo, store, cmd := setupService(t)
	svc.processWeightMB = 256
	svc.memory = newMemoryBudget(512)
	gc := &gatedCompressor{started: make(chan struct{}, 4), release: make(chan struct{})}
	svc.SetCompressor(gc, testLogger())
	svc.SetDecompressors(passthroughDecompressor{})

	start := func(ext string) {
		imgID := uuid.New()
		if err := repo.Create(context.Background(), &image.Image{ID: imgID, AccountID: cmd.AuthContext.AccountID, Title: "img"}); err != nil {
			t.Fatalf("seeding image: %v", err)
		}
		rawPath := fmt.Sprintf("%s/%s.img%s", cmd.AuthContext.AccountID, imgID, ext)
		store.mu.Lock()
		store.files[rawPath] = []byte("payload")
		store.mu.Unlock()
		go svc.runCompression(context.Background(), cmd.AuthContext.AccountID, imgID, rawPath, "img.img"+ext, 7, "")
	}

	// Two transcodes: only one may run.
	start(".xz")
	<-gc.started
	start(".xz")
	select {
	case <-gc.started:
		t.Fatal("a second transcode started inside a one-transcode budget")
	case <-time.After(100 * time.Millisecond):
	}
	close(gc.release)
	<-gc.started // the second, once the first released

	// Two plain compressions fit together in the same budget.
	waitIdle(t, svc.memory)
	gc2 := &gatedCompressor{started: make(chan struct{}, 4), release: make(chan struct{})}
	svc.SetCompressor(gc2, testLogger())
	start("")
	start("")
	<-gc2.started
	<-gc2.started
	close(gc2.release)
	waitIdle(t, svc.memory)
}

func waitIdle(t *testing.T, b *memoryBudget) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b.inUse() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("budget still holds %d MiB", b.inUse())
}

// passthroughDecompressor copies its input, so a ".xz" source transcodes
// without a real codec.
type passthroughDecompressor struct{}

func (passthroughDecompressor) Decompress(_ context.Context, dst io.Writer, src io.Reader) error {
	_, err := io.Copy(dst, src)
	return err
}

func (passthroughDecompressor) Extension() string { return "xz" }
