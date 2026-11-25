package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeApplier records every enqueue and can block or fail.
type fakeApplier struct {
	mu       sync.Mutex
	enqueued []uuid.UUID
	failFor  map[uuid.UUID]bool
	delay    time.Duration
	sawCtx   []error // ctx.Err() observed per call, to prove the context is detached
}

func (f *fakeApplier) EnqueueApplyAccessConfig(ctx context.Context, _ uuid.UUID, nodeID uuid.UUID) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawCtx = append(f.sawCtx, ctx.Err())
	if f.failFor[nodeID] {
		return errors.New("node is not approved")
	}
	f.enqueued = append(f.enqueued, nodeID)
	return nil
}

func (f *fakeApplier) ReapplyAccessConfig(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

func (f *fakeApplier) ApplyAccessConfigOffline(context.Context, uuid.UUID, uuid.UUID, string, int) error {
	return nil
}

func (f *fakeApplier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.enqueued)
}

func (f *fakeApplier) contextErrors() []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]error, len(f.sawCtx))
	copy(out, f.sawCtx)
	return out
}

func propagationService(applier AccessApplier) *Service {
	return &Service{
		applier: applier,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func nodeIDs(n int) []uuid.UUID {
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, uuid.New())
	}
	return ids
}

func waitForCount(t *testing.T, applier *fakeApplier, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for applier.count() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := applier.count(); got != want {
		t.Fatalf("enqueued %d of %d nodes", got, want)
	}
}

// Run inside the HTTP request, this loop would make revoking an SSH key on a
// 300-node account exceed any proxy read timeout — a 504 on a REVOCATION that
// had partially happened. The caller must return immediately.
func TestTheCallerIsNotHeldByTheFanOut(t *testing.T) {
	applier := &fakeApplier{delay: 20 * time.Millisecond}
	svc := propagationService(applier)
	ids := nodeIDs(30) // 600ms of work if it were inline

	start := time.Now()
	svc.dispatchDetached(context.Background(), "test", uuid.New(), ids)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("the caller was held for %s; the fan-out is still inline", elapsed)
	}
	waitForCount(t, applier, len(ids))
}

// The trap this fix could easily have walked into: inheriting the request context
// means the propagation is cancelled the moment the response is written, i.e. the
// same bug with a faster error page.
func TestTheDispatchSurvivesTheRequestBeingCancelled(t *testing.T) {
	applier := &fakeApplier{delay: 5 * time.Millisecond}
	svc := propagationService(applier)
	ids := nodeIDs(10)

	ctx, cancel := context.WithCancel(context.Background())
	svc.dispatchDetached(ctx, "test", uuid.New(), ids)
	cancel() // the handler has returned and the request context is done

	waitForCount(t, applier, len(ids))
	for i, err := range applier.contextErrors() {
		if err != nil {
			t.Fatalf("call %d saw a cancelled context (%v); the propagation is not detached", i, err)
		}
	}
}

// One node that cannot accept the command must not abandon the rest — the
// remaining nodes are the ones still holding the revoked key.
func TestOneFailingNodeDoesNotStopTheRest(t *testing.T) {
	ids := nodeIDs(5)
	applier := &fakeApplier{failFor: map[uuid.UUID]bool{ids[0]: true, ids[3]: true}}
	svc := propagationService(applier)

	svc.dispatchDetached(context.Background(), "test", uuid.New(), ids)

	waitForCount(t, applier, 3)
}

func TestNothingToDoDispatchesNothing(t *testing.T) {
	applier := &fakeApplier{}
	svc := propagationService(applier)

	svc.dispatchDetached(context.Background(), "test", uuid.New(), nil)

	time.Sleep(20 * time.Millisecond)
	if applier.count() != 0 {
		t.Fatalf("enqueued %d commands for an empty node set", applier.count())
	}
}

// Without an applier wired, mutations are stored but not pushed — the documented
// behaviour, and it must not panic on a nil interface.
func TestNoApplierIsNotAPanic(t *testing.T) {
	svc := propagationService(nil)
	svc.dispatchDetached(context.Background(), "test", uuid.New(), nodeIDs(3))
	time.Sleep(20 * time.Millisecond)
}
