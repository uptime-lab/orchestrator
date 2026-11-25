package workflow_execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
)

// A dropped enqueue is not a delayed step: the execution node stays pending,
// no worker ever touches it again, and the execution keeps completed_at NULL —
// consuming an active-cap slot that retention will never sweep.

type fakeActiveIDsRepo struct {
	repository.WorkflowExecutionNodeRepository
	ids   []uuid.UUID
	err   error
	calls int
}

func (f *fakeActiveIDsRepo) GetActiveIDs(context.Context) ([]uuid.UUID, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.ids, nil
}

func sweepTestService(queueSize int, repo repository.WorkflowExecutionNodeRepository) *Service {
	return &Service{
		nodeQueue:         make(chan uuid.UUID, queueSize),
		executionNodeRepo: repo,
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestEnqueueOnAFullQueueArmsTheSweeper(t *testing.T) {
	svc := sweepTestService(1, &fakeActiveIDsRepo{})

	if !svc.enqueueNode(uuid.New(), "first") {
		t.Fatal("the first enqueue should fit")
	}
	if svc.droppedEnqueue.Load() {
		t.Error("a successful enqueue must not arm the sweeper")
	}

	if svc.enqueueNode(uuid.New(), "overflow") {
		t.Fatal("the queue is full; this enqueue cannot have been accepted")
	}
	if !svc.droppedEnqueue.Load() {
		t.Error("a dropped enqueue left no trace — the node is stranded forever")
	}
}

// The whole point: the id that was dropped comes back. The sweep re-enqueues
// from the database rather than from anything it remembered, because the queue
// is in-memory and the database is the only record of what still needs doing.
func TestSweepRecoversDroppedWorkFromTheDatabase(t *testing.T) {
	stranded := uuid.New()
	repo := &fakeActiveIDsRepo{ids: []uuid.UUID{stranded}}
	svc := sweepTestService(4, repo)

	svc.recoverDroppedNodes(context.Background())

	select {
	case got := <-svc.nodeQueue:
		if got != stranded {
			t.Fatalf("re-enqueued %v, want the stranded node %v", got, stranded)
		}
	default:
		t.Fatal("the sweep enqueued nothing; the stranded node is still stranded")
	}
}

// A sweep that cannot read the database must re-arm itself. Clearing the flag
// and then failing would turn a transient outage into permanently lost work —
// the exact failure mode being fixed, one level up.
func TestFailedSweepRearms(t *testing.T) {
	repo := &fakeActiveIDsRepo{err: errors.New("database is down")}
	svc := sweepTestService(4, repo)
	svc.droppedEnqueue.Store(false)

	svc.recoverDroppedNodes(context.Background())

	if !svc.droppedEnqueue.Load() {
		t.Error("a failed sweep cleared the flag; the next tick will not retry")
	}
}
