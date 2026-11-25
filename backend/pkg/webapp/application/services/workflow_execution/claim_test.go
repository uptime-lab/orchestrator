package workflow_execution

import (
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// claimTestService builds a service with only what processNode and
// scheduleReenqueue touch: the queue, the logger and the two sync.Maps. The
// graph walk itself is replaced per test, so these are about the claim and the
// timer, not about the engine.
func claimTestService(queueSize int) *Service {
	return &Service{
		nodeQueue: make(chan uuid.UUID, queueSize),
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// These drive withNodeClaim with a stubbed walk: the claim is what is under test,
// and the real graph step needs a database.

// A concurrent double-dispatch is the whole risk: two of the 500 workers
// loading the same row, both advancing it, and dispatchAgent registering a
// command each — of erase_drive, install_os, capture_image or reboot.
func TestOnlyOneWorkerWalksAnExecutionNodeAtATime(t *testing.T) {
	svc := claimTestService(16)
	id := uuid.New()

	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	release := make(chan struct{})
	walk := func() {
		now := concurrent.Add(1)
		for {
			was := maxConcurrent.Load()
			if now <= was || maxConcurrent.CompareAndSwap(was, now) {
				break
			}
		}
		<-release
		concurrent.Add(-1)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			svc.withNodeClaim(id, worker, walk)
		}(i)
	}

	// Give every goroutine time to reach the claim before releasing the owner.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := maxConcurrent.Load(); got != 1 {
		t.Fatalf("%d workers walked the same execution node at once", got)
	}
}

// A worker turned away by the claim must not have its wake-up lost: the event it
// carried (a command completing) is the reason the node has to be re-walked.
func TestAWakeUpArrivingDuringAWalkIsNotLost(t *testing.T) {
	svc := claimTestService(16)
	id := uuid.New()

	var walks atomic.Int32
	firstWalkStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	walk := func() {
		if walks.Add(1) == 1 {
			close(firstWalkStarted)
			<-releaseFirst
		}
	}

	done := make(chan struct{})
	go func() {
		svc.withNodeClaim(id, 1, walk)
		close(done)
	}()

	<-firstWalkStarted
	// A second worker arrives mid-walk and is turned away.
	svc.withNodeClaim(id, 2, walk)
	close(releaseFirst)
	<-done

	if got := walks.Load(); got != 2 {
		t.Fatalf("the graph was walked %d times, want 2 — the second wake-up was lost", got)
	}
}

// Several arrivals during one walk collapse into ONE extra pass: the point is
// that the node is re-walked, not that it is re-walked per arrival.
func TestManyArrivalsDuringAWalkCollapseIntoOneRerun(t *testing.T) {
	svc := claimTestService(16)
	id := uuid.New()

	var walks atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	walk := func() {
		if walks.Add(1) == 1 {
			close(started)
			<-release
		}
	}

	done := make(chan struct{})
	go func() {
		svc.withNodeClaim(id, 1, walk)
		close(done)
	}()

	<-started
	for i := 0; i < 20; i++ {
		svc.withNodeClaim(id, i+2, walk)
	}
	close(release)
	<-done

	if got := walks.Load(); got != 2 {
		t.Fatalf("the graph was walked %d times, want 2", got)
	}
}

// After the owner is gone the node must be claimable again, or a workflow stalls
// forever on the first step it took.
func TestTheClaimIsReleasedAfterTheWalk(t *testing.T) {
	svc := claimTestService(16)
	id := uuid.New()

	var walks atomic.Int32
	walk := func() { walks.Add(1) }

	svc.withNodeClaim(id, 1, walk)
	svc.withNodeClaim(id, 2, walk)

	if got := walks.Load(); got != 2 {
		t.Fatalf("the graph was walked %d times across two sequential calls, want 2", got)
	}
	if _, held := svc.inFlight.Load(id); held {
		t.Fatal("the claim was not released")
	}
}

// The re-arm path recomputed the SAME absolute deadline on every pass, and each
// armed timer re-enqueues the node, which arms another. Repeated arming of one
// deadline must produce one timer.
func TestTheSameDeadlineIsArmedOnce(t *testing.T) {
	svc := claimTestService(64)
	id := uuid.New()

	for i := 0; i < 25; i++ {
		svc.scheduleReenqueue(id, 40*time.Millisecond)
	}

	time.Sleep(200 * time.Millisecond)
	if got := len(svc.nodeQueue); got != 1 {
		t.Fatalf("%d re-enqueues fired for one deadline armed 25 times", got)
	}
}

// An EARLIER deadline must still be armed: the guard is "one pending timer that
// fires no later than needed", not "one timer ever".
func TestAnEarlierDeadlineIsStillArmed(t *testing.T) {
	svc := claimTestService(64)
	id := uuid.New()

	svc.scheduleReenqueue(id, 2*time.Second)
	svc.scheduleReenqueue(id, 30*time.Millisecond)

	time.Sleep(200 * time.Millisecond)
	if got := len(svc.nodeQueue); got != 1 {
		t.Fatalf("the earlier deadline did not fire on its own schedule (queue holds %d)", got)
	}
}

// Once a timer has fired, the next pass may arm again — otherwise a block whose
// deadline moves (a wait re-entered) would never be re-checked.
func TestAFiredDeadlineCanBeArmedAgain(t *testing.T) {
	svc := claimTestService(64)
	id := uuid.New()

	svc.scheduleReenqueue(id, 20*time.Millisecond)
	time.Sleep(120 * time.Millisecond)
	svc.scheduleReenqueue(id, 20*time.Millisecond)
	time.Sleep(120 * time.Millisecond)

	if got := len(svc.nodeQueue); got != 2 {
		t.Fatalf("the queue holds %d re-enqueues, want 2", got)
	}
}
