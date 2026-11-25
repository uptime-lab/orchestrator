package workflow_execution

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// countingExecutionRepo keeps one stored row and hands out the copy a worker
// would have read: the creation loop enqueues each execution node BEFORE the
// count is known, so the worker's copy legitimately carries node_count 0 while
// the stored row already carries the real count.
type countingExecutionRepo struct {
	repository.WorkflowExecutionRepository
	stored *workflow_execution.WorkflowExecution
	// handOut is what GetByID answers — the stale copy, as a worker would hold.
	handOut *workflow_execution.WorkflowExecution
	// fullRowWrites counts Update calls, which is the operation that caused this.
	fullRowWrites int
}

func (r *countingExecutionRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*workflow_execution.WorkflowExecution, error) {
	copied := *r.handOut
	return &copied, nil
}

func (r *countingExecutionRepo) Update(_ context.Context, e *workflow_execution.WorkflowExecution) error {
	r.fullRowWrites++
	copied := *e
	r.stored = &copied
	return nil
}

func (r *countingExecutionRepo) MarkStarted(_ context.Context, _, _ uuid.UUID, startedAt time.Time) (*workflow_execution.WorkflowExecution, error) {
	if r.stored.Status == workflow_execution.StatusPending {
		r.stored.Status = workflow_execution.StatusRunning
		r.stored.StartedAt = &startedAt
	}
	copied := *r.stored
	return &copied, nil
}

// The engine's first pass must not revert node_count.
//
// begin() wrote the WHOLE execution row from the copy the worker was handed, and
// that copy was read before StartExecution's UpdateNodeCount landed — so a
// 50-node run reported "0 nodes" for the rest of its life. The count is what the
// run page shows and what an operator uses to tell a fan-out from a single-node
// run.
func TestEngineBeginDoesNotRevertTheNodeCount(t *testing.T) {
	accountID, execID, execNodeID := uuid.New(), uuid.New(), uuid.New()
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "b1", Type: "command." + node_command_domain.SlugReboot},
		},
		Edges: []workflow_template.GraphEdge{{ID: "e1", From: "start", FromPort: "out", To: "b1"}},
	}
	snapshot := &workflow_template.TemplateSnapshot{TemplateID: uuid.New(), Name: "nightly", Graph: graph}

	stored := &workflow_execution.WorkflowExecution{
		ID: execID, AccountID: accountID,
		Status: workflow_execution.StatusPending, NodeCount: 50,
		TemplateSnapshot: snapshot,
	}
	// What the worker holds: the same run, read before the count was written.
	stale := *stored
	stale.NodeCount = 0

	execRepo := &countingExecutionRepo{stored: stored, handOut: &stale}
	svc := &Service{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		executionNodeRepo: &firstEntryNodeRepo{id: execNodeID, accountID: accountID, execID: execID},
		executionRepo:     execRepo,
		nodeRepo:          &oneNodeRepo{},
		nodeCommandRepo:   &linkedCommandRepo{},
		nodeService:       &countingNodeService{},
	}

	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if execRepo.stored.NodeCount != 50 {
		t.Fatalf("node_count = %d after the first pass, want 50 — the run now reports zero nodes for the rest of its life", execRepo.stored.NodeCount)
	}
	if execRepo.stored.Status != workflow_execution.StatusRunning {
		t.Errorf("status = %q, want %q: the run must still be marked started", execRepo.stored.Status, workflow_execution.StatusRunning)
	}
	if execRepo.stored.StartedAt == nil {
		t.Error("started_at was never stamped")
	}
}
