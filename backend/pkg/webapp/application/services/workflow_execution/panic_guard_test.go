package workflow_execution

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

// These two tests cover the pair of changes that only make sense together: the
// worker's recover(), and the claim release that recover() would otherwise
// turn into a permanent stall.
//
// Both build the Service as a bare struct with nil repositories. That is not
// laziness — a nil embedded repository is precisely how a panic gets produced
// here, so the fakes would have to be written to misbehave anyway.

func newGuardTestService() *Service {
	return &Service{logger: slog.New(slog.DiscardHandler)}
}

// A panic inside the graph walk must not leave the execution node claimed.
//
// This is the failure the recover() would have created: withNodeClaim retired
// the claim at the END of its loop, so a panic skipped it. While a panic killed
// the process that was invisible (the map is in-memory and died with it); once
// the worker recovers, the claim outlives the panic and every later enqueue
// finds it, marks it dirty and returns — the node never advances again, its run
// never reaches completed_at, and it holds an active-execution slot forever.
func TestNodeClaimIsReleasedWhenTheWalkPanics(t *testing.T) {
	s := newGuardTestService()
	id := uuid.New()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("the panic should still propagate out of withNodeClaim itself")
			}
		}()
		s.withNodeClaim(id, 1, func() { panic("boom") })
	}()

	if _, stillClaimed := s.inFlight.Load(id); stillClaimed {
		t.Fatal("the claim must be released when the walk panics, or the node stalls forever")
	}

	// And the node must be claimable again — the real consequence of the leak.
	walked := false
	s.withNodeClaim(id, 2, func() { walked = true })
	if !walked {
		t.Fatal("a later worker must be able to claim the node again")
	}
}

// A panic while advancing a node must not escape the worker. If it does, it
// kills the process — and because Start() re-enqueues every in-flight node on
// boot, the same node panics again on restart: an unbounded crash loop that
// takes the HTTP API and every agent stream down with it.
func TestAPanicWhileAdvancingANodeDoesNotEscape(t *testing.T) {
	s := newGuardTestService()
	id := uuid.New()

	// nil executionNodeRepo: the walk panics on the first repository call, and
	// so does the settle path that runs from inside the deferred recover —
	// which is the case the second, inner recover exists for.
	s.processNodeGuarded(context.Background(), id, 1)

	if _, stillClaimed := s.inFlight.Load(id); stillClaimed {
		t.Fatal("a recovered panic must not leave the node claimed")
	}
}

// Recovery must be per-item, not per-worker: one poisoned node cannot be
// allowed to stop the worker from serving every other node.
func TestWorkerKeepsServingAfterARecoveredPanic(t *testing.T) {
	s := newGuardTestService()

	for i := 0; i < 3; i++ {
		s.processNodeGuarded(context.Background(), uuid.New(), 1)
	}

	// Reaching this line is most of the assertion: three consecutive panics
	// were contained rather than unwinding out of the loop. The rest is that
	// none of them leaked a claim.
	leaked := 0
	s.inFlight.Range(func(any, any) bool {
		leaked++
		return true
	})
	if leaked != 0 {
		t.Fatalf("%d claims leaked across recovered panics", leaked)
	}
}
