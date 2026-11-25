package workflow_execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// storedExecutionRepo keeps one row. Update writes what it is told, column for
// column — deliberately WITHOUT the repository's refusal to write over a
// completed run, so this test measures the service on its own: it must not hand
// a stale full row to a full-row write in the first place. The refusal is the
// second, independent guard and is covered against real Postgres in
// workflow_execution_write_integration_test.go.
type storedExecutionRepo struct {
	repository.WorkflowExecutionRepository
	stored *workflow_execution.WorkflowExecution
}

func (r *storedExecutionRepo) Create(_ context.Context, e *workflow_execution.WorkflowExecution) error {
	copied := *e
	r.stored = &copied
	return nil
}

func (r *storedExecutionRepo) Update(_ context.Context, e *workflow_execution.WorkflowExecution) error {
	if r.stored == nil {
		return app_errors.ErrNotFound
	}
	copied := *e
	r.stored = &copied
	return nil
}

func (r *storedExecutionRepo) UpdateNodeCount(_ context.Context, _, _ uuid.UUID, nodeCount int) error {
	if r.stored == nil {
		return app_errors.ErrNotFound
	}
	r.stored.NodeCount = nodeCount
	return nil
}

// finishingExecNodeRepo stands in for the worker pool: the engine is handed each
// execution node inside the creation loop, so by the time StartExecution writes
// the node count the run can already be over. Completing it here is the earliest
// point that race can be expressed deterministically.
type finishingExecNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	execution *storedExecutionRepo
}

func (r *finishingExecNodeRepo) Create(_ context.Context, _ *workflow_execution_node.WorkflowExecutionNode) error {
	if r.execution.stored != nil {
		r.execution.stored.Complete(workflow_execution.StatusCompleted)
	}
	return nil
}

// A run that finishes while StartExecution is still working must stay finished.
// The trailing node-count write used to be a full-row write from the copy made
// before the workers were handed the run, so it put `pending` and a null
// completed_at back — and completed_at is what the active-executions cap counts
// and what retention sweeps, so the run held a cap slot for good.
func TestStartExecutionDoesNotReopenARunThatAlreadyFinished(t *testing.T) {
	accountID := uuid.New()
	nodeID := uuid.New()
	template := &workflow_template.WorkflowTemplate{
		ID:        uuid.New(),
		AccountID: accountID,
		Name:      "playbook",
		Status:    workflow_template.StatusActive,
	}
	executions := &storedExecutionRepo{}

	svc := &Service{
		templateRepo:      &stubTemplateRepo{template: template},
		nodeRepo:          &stubScopeNodeRepo{nodes: map[uuid.UUID]*node.Node{nodeID: {ID: nodeID, AccountID: accountID}}},
		executionRepo:     executions,
		executionNodeRepo: &finishingExecNodeRepo{execution: executions},
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	cmd := workflow_execution_start.WorkflowExecutionStartCommand{
		TemplateID:  template.ID,
		NodeIDs:     []uuid.UUID{nodeID},
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}

	if _, err := svc.StartExecution(context.Background(), cmd); err != nil {
		t.Fatalf("start: %v", err)
	}

	if executions.stored.CompletedAt == nil {
		t.Error("the run was reopened: completed_at is nil, so it holds an active-execution slot and retention will never sweep it")
	}
	if executions.stored.Status != workflow_execution.StatusCompleted {
		t.Errorf("status = %q, want the outcome the worker recorded (%q)",
			executions.stored.Status, workflow_execution.StatusCompleted)
	}
	if executions.stored.NodeCount != 1 {
		t.Errorf("node_count = %d, want 1 — the count must still land", executions.stored.NodeCount)
	}
}

// The zero-node path settles the run itself, so its write must still go through.
// It is reached by a scheduled fire whose targets were all deleted between
// resolving the scope and starting; a member's run with an unknown target is
// refused before any row exists (TestStartExecutionRefusesAnyUnknownTarget).
func TestStartExecutionWithNoValidNodesStillFailsTheRun(t *testing.T) {
	accountID := uuid.New()
	template := &workflow_template.WorkflowTemplate{
		ID:        uuid.New(),
		AccountID: accountID,
		Name:      "playbook",
		Status:    workflow_template.StatusActive,
	}
	executions := &storedExecutionRepo{}

	svc := &Service{
		templateRepo:      &stubTemplateRepo{template: template},
		nodeRepo:          &stubScopeNodeRepo{nodes: map[uuid.UUID]*node.Node{}},
		executionRepo:     executions,
		executionNodeRepo: &finishingExecNodeRepo{execution: executions},
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	cmd := workflow_execution_start.WorkflowExecutionStartCommand{
		TemplateID:  template.ID,
		NodeIDs:     []uuid.UUID{uuid.New()},
		AuthContext: commands.NewAuthContext(accountID, uuid.Nil),
		Scheduled:   true,
	}

	_, err := svc.StartExecution(context.Background(), cmd)
	if !errors.Is(err, app_errors.ErrNoValidTargetNodes) {
		t.Fatalf("start returned %v, want ErrNoValidTargetNodes", err)
	}
	if executions.stored.CompletedAt == nil {
		t.Error("a run with no valid targets was left unfinished")
	}
}

// A member's run names its targets; one that is not in the account (another
// account's node and a nonexistent id look the same) refuses the whole run
// before any execution row exists, instead of starting on the rest.
func TestStartExecutionRefusesAnyUnknownTarget(t *testing.T) {
	accountID := uuid.New()
	known := uuid.New()
	template := &workflow_template.WorkflowTemplate{
		ID:        uuid.New(),
		AccountID: accountID,
		Name:      "playbook",
		Status:    workflow_template.StatusActive,
	}
	executions := &storedExecutionRepo{}
	svc := &Service{
		templateRepo:      &stubTemplateRepo{template: template},
		nodeRepo:          &stubScopeNodeRepo{nodes: map[uuid.UUID]*node.Node{known: {ID: known, AccountID: accountID}}},
		executionRepo:     executions,
		executionNodeRepo: &finishingExecNodeRepo{execution: executions},
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	_, err := svc.StartExecution(context.Background(), workflow_execution_start.WorkflowExecutionStartCommand{
		TemplateID:  template.ID,
		NodeIDs:     []uuid.UUID{known, uuid.New()},
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	})
	if !errors.Is(err, app_errors.ErrNodeNotFound) {
		t.Fatalf("start returned %v, want ErrNodeNotFound", err)
	}
	if executions.stored != nil {
		t.Fatal("a refused run left an execution row behind")
	}
}
