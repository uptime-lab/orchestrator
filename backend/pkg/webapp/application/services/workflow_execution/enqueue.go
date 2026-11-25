package workflow_execution

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// The work queue is in-memory and every send to it was a
// non-blocking `select` with a bare `default:` that logged and moved on. A
// dropped id is not a delayed step, it is a permanent one: the execution node
// stays `pending`, no worker will ever touch it again, and the execution keeps
// `completed_at` NULL — so it consumes one of the account's active-execution
// slots and is never swept by retention, which keys on that same field. That is
// exactly the zombie abandon.go settles from the other end (a deleted node), reachable
// here without deleting anything. One cron playbook over fifty nodes is enough.
//
// Why not simply block on the send: three of the four call sites run ON the
// path that drains the queue — the claim hand-back, the command-completion
// re-enqueue and the wait-timer callback — so a blocking send at a full queue
// would deadlock the very workers that empty it. Growing the buffer only moves
// the cliff.
//
// So the send stays non-blocking and the DROP becomes recoverable: a drop
// raises a flag, and a sweeper re-enqueues every in-flight execution node the
// same way a restart does. Engine re-entry is idempotent (the same path
// ProcessNodeCommandCompletion uses) and the claim mechanism keeps a node from
// being processed twice, so re-enqueueing one that was merely waiting is safe.
// Nothing runs while the flag is clear, which is the normal case.

const (
	// dropSweepInterval bounds how long a dropped id can sit unprocessed. The
	// queue only fills under a burst, and a burst drains in seconds; this is
	// the recovery path, not the fast path.
	dropSweepInterval = 30 * time.Second
)

// enqueueNode offers an execution node to the workers. It reports whether the
// id was accepted; a false return has already been recorded for the sweeper.
func (s *Service) enqueueNode(executionNodeID uuid.UUID, reason string) bool {
	select {
	case s.nodeQueue <- executionNodeID:
		return true
	default:
		// Deliberately not a Warn per drop: a full queue drops in bursts, and
		// the interesting event is "work was dropped and recovered", which the
		// sweeper logs once.
		s.droppedEnqueue.Store(true)
		s.logger.Warn("node queue full; enqueue deferred to the drop sweeper",
			"execution_node_id", executionNodeID, "reason", reason)
		return false
	}
}

// startDropSweeper re-enqueues in-flight execution nodes after any drop.
//
// It is armed only by an actual drop, so a healthy install never runs the
// query. The sweep itself is the same one Start() performs after a restart,
// for the same reason: the queue is in-memory, so the database is the only
// record of what still needs doing.
func (s *Service) startDropSweeper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(dropSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Swap, not Load-then-Store: a drop landing during the sweep
				// must arm the NEXT one rather than be swallowed by this one.
				if !s.droppedEnqueue.Swap(false) {
					continue
				}
				s.recoverDroppedNodes(ctx)
			}
		}
	}()
}

func (s *Service) recoverDroppedNodes(ctx context.Context) {
	ids, err := s.executionNodeRepo.GetActiveIDs(ctx)
	if err != nil {
		// Re-arm: the work is still stranded, and the next tick must retry.
		s.droppedEnqueue.Store(true)
		s.logger.ErrorContext(ctx, "drop sweep could not list in-flight execution nodes", "error", err)
		return
	}
	requeued := 0
	for _, id := range ids {
		select {
		case s.nodeQueue <- id:
			requeued++
		case <-ctx.Done():
			return
		}
	}
	s.logger.InfoContext(ctx, "drop sweep re-enqueued in-flight execution nodes",
		"count", requeued, "candidates", len(ids))
}
