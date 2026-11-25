package sql

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// The property under test: N captures started at the same instant must
// not all pass the same cap check. This is exactly what a fake cannot prove —
// the serialization comes from `pg_advisory_xact_lock` inside a transaction, so
// only a real Postgres can say whether the check and the insert are one step.
//
// Reading the count in the service and inserting afterwards, or excluding
// `capturing` rows from the count, would let N concurrent captures all pass a
// cap smaller than N.
func TestConcurrentCapturesAdmitExactlyTheCap(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	const (
		racers = 9
		limit  = 3
		window = time.Hour
	)
	var admitted int32
	var wg sync.WaitGroup
	wg.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start
			img := image.NewCaptureImage(uuid.New(), accountID, "capture", false, uuid.New(), "node")
			err := repo.CreateCaptureUnderCap(ctx, img, limit, window)
			switch {
			case err == nil:
				atomic.AddInt32(&admitted, 1)
			case errors.Is(err, app_errors.ErrImageLimitReached):
				// the expected refusal
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if admitted != limit {
		t.Errorf("%d of %d concurrent captures were admitted, want exactly %d", admitted, racers, limit)
	}

	count, err := repo.CountQuotaOccupying(ctx, accountID, window, nil)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != limit {
		t.Errorf("account holds %d quota-occupying images, want %d", count, limit)
	}
}

// The user-facing create path must hold its cap the same way the capture path
// does. It was the last unlocked check-then-insert over this cap: N concurrent
// POST /images all counted the same rows in the service before any of them had
// inserted, and all N passed a cap of 3.
func TestConcurrentCreatesAdmitExactlyTheCap(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	const (
		racers = 9
		limit  = 3
		window = time.Hour
	)
	var admitted int32
	var wg sync.WaitGroup
	wg.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start
			img := image.NewImage(accountID, "user upload", "", "", "")
			err := repo.CreateUnderCap(ctx, img, limit, window, 0, 0)
			switch {
			case err == nil:
				atomic.AddInt32(&admitted, 1)
			case errors.Is(err, app_errors.ErrImageLimitReached):
				// the expected refusal
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if admitted != limit {
		t.Errorf("%d of %d concurrent creates were admitted, want exactly %d", admitted, racers, limit)
	}
}

// The byte ceilings are inside the same lock as the count: a full account is
// refused before a record exists, and a second create right behind the first
// sees the same answer. The stored sum counts the current file PLUS a retained
// pre-compression original, mirroring storedBytesExpr.
func TestCreateUnderCapRefusesWhenTheByteQuotaIsFull(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()
	const gib = int64(1) << 30

	full := image.NewImage(accountID, "stored.img", "", "", "")
	full.FileStorageInfo = &image.FileStorageInfo{SizeBytes: 6 * gib, SourceSizeBytes: 4 * gib}
	if err := repo.Create(ctx, full); err != nil {
		t.Fatalf("seeding stored image: %v", err)
	}

	for i := 0; i < 2; i++ {
		img := image.NewImage(accountID, "no room", "", "", "")
		err := repo.CreateUnderCap(ctx, img, 0, time.Hour, 10*gib, 0)
		if !errors.Is(err, app_errors.ErrImageLimitReached) {
			t.Fatalf("create %d over a full byte quota: got %v, want ErrImageLimitReached", i, err)
		}
	}

	// Another account is unaffected by this account's ceiling but bound by the
	// instance one, which sums every tenant.
	other := image.NewImage(uuid.New(), "other tenant", "", "", "")
	if err := repo.CreateUnderCap(ctx, other, 0, time.Hour, 10*gib, 0); err != nil {
		t.Fatalf("another account must not be capped by this one's bytes: %v", err)
	}
	instanceBound := image.NewImage(uuid.New(), "instance full", "", "", "")
	err := repo.CreateUnderCap(ctx, instanceBound, 0, time.Hour, 0, 10*gib)
	if !errors.Is(err, app_errors.ErrImageLimitReached) {
		t.Fatalf("create over a full instance ceiling: got %v, want ErrImageLimitReached", err)
	}
}

// With no cap configured CreateUnderCap must be a plain insert — an install
// may set none of the three, and a refusal there would brick creation.
func TestCreateUnderCapUnlimitedIsAPlainInsert(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()

	img := image.NewImage(uuid.New(), "unbounded", "", "", "")
	if err := repo.CreateUnderCap(ctx, img, 0, time.Hour, 0, 0); err != nil {
		t.Fatalf("create with no caps configured: %v", err)
	}
}

// The other half of the same decision: an abandoned capture must age out, or the
// fix trades the race for the brick the original count query was written to
// avoid (permanent, on an install where MaxPerAccount is 1).
func TestStaleCaptureStopsOccupyingItsSlot(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	stale := image.NewCaptureImage(uuid.New(), accountID, "abandoned", false, uuid.New(), "node")
	stale.CreatedAt = time.Now().Add(-2 * time.Hour)
	stale.UpdatedAt = stale.CreatedAt
	if err := repo.Create(ctx, stale); err != nil {
		t.Fatalf("seeding stale capture: %v", err)
	}

	// Inside the window it counts...
	count, err := repo.CountQuotaOccupying(ctx, accountID, 4*time.Hour, nil)
	if err != nil {
		t.Fatalf("counting inside the window: %v", err)
	}
	if count != 1 {
		t.Errorf("count inside the window = %d, want 1", count)
	}

	// ...outside it, it does not, so a capture can still be created at cap 1.
	count, err = repo.CountQuotaOccupying(ctx, accountID, time.Hour, nil)
	if err != nil {
		t.Fatalf("counting outside the window: %v", err)
	}
	if count != 0 {
		t.Errorf("count outside the window = %d, want 0", count)
	}
	fresh := image.NewCaptureImage(uuid.New(), accountID, "fresh", false, uuid.New(), "node")
	if err := repo.CreateCaptureUnderCap(ctx, fresh, 1, time.Hour); err != nil {
		t.Fatalf("an abandoned capture bricked image creation: %v", err)
	}
}

// A failed capture never counts, at any window: nothing is going to arrive and
// it holds no stored bytes. It is also the state a refused finalize leaves
// behind, so counting it would make one refusal cost the account a slot.
func TestFailedCaptureNeverOccupiesASlot(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	failed := image.NewCaptureImage(uuid.New(), accountID, "failed", false, uuid.New(), "node")
	if err := repo.Create(ctx, failed); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := repo.SetCompressionState(ctx, accountID, failed.ID, image.CaptureFailed, 0); err != nil {
		t.Fatalf("marking failed: %v", err)
	}

	count, err := repo.CountQuotaOccupying(ctx, accountID, time.Hour, nil)
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 0 {
		t.Errorf("a failed capture counted %d, want 0", count)
	}
}

// A finished image always counts, whatever the window is — the window governs
// captures in flight, not storage. Guards against a scope predicate that
// accidentally makes the whole cap conditional on the window.
func TestFinishedImagesCountAtAnyWindow(t *testing.T) {
	repo := imageRepoForClaimTest(t)
	ctx := context.Background()
	accountID := uuid.New()

	img := image.NewImage(accountID, "raw.img", "", "", "")
	if err := repo.Create(ctx, img); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	for _, window := range []time.Duration{0, time.Hour} {
		count, err := repo.CountQuotaOccupying(ctx, accountID, window, nil)
		if err != nil {
			t.Fatalf("counting at window %v: %v", window, err)
		}
		if count != 1 {
			t.Errorf("count at window %v = %d, want 1", window, count)
		}
	}

	// ...and excluding it by id leaves nothing, which is what the finalize
	// backstop relies on so a record never counts itself.
	count, err := repo.CountQuotaOccupying(ctx, accountID, time.Hour, &img.ID)
	if err != nil {
		t.Fatalf("counting with an exclusion: %v", err)
	}
	if count != 0 {
		t.Errorf("count excluding the only image = %d, want 0", count)
	}
}
