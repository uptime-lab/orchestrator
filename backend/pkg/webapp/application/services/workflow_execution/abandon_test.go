package workflow_execution

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
)

// AbandonNodeExecutions, called from node deletion, is what keeps a deleted
// node from pinning its execution "active" forever; these tests pin it. If
// the execution node's only settler ran on RECONNECT — which a deleted node
// never does — completed_at would stay NULL, the run would count against the
// per-account active cap, and retention (which keys on completed_at) would
// never sweep it. A cap's worth of such runs would end playbooks for the
// account with no API remedy.

// --- fakes ----------------------------------------------------------------

type abandonExecRepo struct {
	repository.WorkflowExecutionRepository
	executions map[uuid.UUID]*workflow_execution.WorkflowExecution
	updates    int
}

func (r *abandonExecRepo) GetByID(_ context.Context, accountID, executionID uuid.UUID) (*workflow_execution.WorkflowExecution, error) {
	e, ok := r.executions[executionID]
	if !ok || e.AccountID != accountID {
		return nil, app_errors.ErrNotFound
	}
	copied := *e
	return &copied, nil
}

func (r *abandonExecRepo) Update(_ context.Context, e *workflow_execution.WorkflowExecution) error {
	copied := *e
	r.executions[e.ID] = &copied
	r.updates++
	return nil
}

type abandonExecNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	byNode map[uuid.UUID][]workflow_execution_node.WorkflowExecutionNode
	byExec map[uuid.UUID][]workflow_execution_node.WorkflowExecutionNode
	// updated records every node the code settled, in order.
	updated []workflow_execution_node.WorkflowExecutionNode
}

// GetActiveByNodeID mirrors the SQL: unfinished rows only. The fake filters for
// the same reason the query does — a fake that returns finished rows would let
// a caller depend on getting them.
func (r *abandonExecNodeRepo) GetActiveByNodeID(_ context.Context, _, nodeID uuid.UUID) ([]workflow_execution_node.WorkflowExecutionNode, error) {
	var active []workflow_execution_node.WorkflowExecutionNode
	for _, n := range r.byNode[nodeID] {
		if n.Status == workflow_execution_node.StatusPending || n.Status == workflow_execution_node.StatusRunning {
			active = append(active, n)
		}
	}
	return active, nil
}

func (r *abandonExecNodeRepo) GetByExecutionID(_ context.Context, executionID uuid.UUID) ([]workflow_execution_node.WorkflowExecutionNode, error) {
	// Return the freshest view: a settled node must read back as settled, or
	// updateExecutionStatus would see the pre-settle state and never finalize.
	nodes := append([]workflow_execution_node.WorkflowExecutionNode(nil), r.byExec[executionID]...)
	for i := range nodes {
		for _, u := range r.updated {
			if u.ID == nodes[i].ID {
				nodes[i] = u
			}
		}
	}
	return nodes, nil
}

func (r *abandonExecNodeRepo) Update(_ context.Context, n *workflow_execution_node.WorkflowExecutionNode) error {
	r.updated = append(r.updated, *n)
	return nil
}

// UpdateActive mirrors the SQL guard: the write lands only while the STORED row
// is still pending or running. The fake enforces it rather than always writing,
// because the whole point of moving the settle onto this method is that a node
// which finished first keeps its own outcome — a fake that wrote unconditionally
// would let that regress silently.
func (r *abandonExecNodeRepo) UpdateActive(_ context.Context, n *workflow_execution_node.WorkflowExecutionNode) (bool, error) {
	stored := r.storedStatus(n.ID)
	if stored != workflow_execution_node.StatusPending && stored != workflow_execution_node.StatusRunning {
		return false, nil
	}
	r.updated = append(r.updated, *n)
	return true, nil
}

// storedStatus is what a fresh read of the row would answer: the last settled
// write if there is one, otherwise the seeded status.
func (r *abandonExecNodeRepo) storedStatus(id uuid.UUID) workflow_execution_node.WorkflowExecutionNodeStatus {
	for i := len(r.updated) - 1; i >= 0; i-- {
		if r.updated[i].ID == id {
			return r.updated[i].Status
		}
	}
	for _, nodes := range r.byExec {
		for _, n := range nodes {
			if n.ID == id {
				return n.Status
			}
		}
	}
	for _, nodes := range r.byNode {
		for _, n := range nodes {
			if n.ID == id {
				return n.Status
			}
		}
	}
	return ""
}

type abandonCommandService struct {
	services.NodeCommandService
	failed map[uuid.UUID]string
}

func (s *abandonCommandService) FailCommandOfStoppedRun(_ context.Context, id uuid.UUID, reason string) error {
	if s.failed == nil {
		s.failed = map[uuid.UUID]string{}
	}
	s.failed[id] = reason
	return nil
}

// --- harness --------------------------------------------------------------

type abandonFixture struct {
	svc       *Service
	execRepo  *abandonExecRepo
	nodeRepo  *abandonExecNodeRepo
	commands  *abandonCommandService
	accountID uuid.UUID
	execID    uuid.UUID
	nodeID    uuid.UUID
}

// newAbandonFixture builds one running execution with a single execution node
// targeting nodeID, optionally blocked on a device command.
func newAbandonFixture(t *testing.T, status workflow_execution_node.WorkflowExecutionNodeStatus, commandID *uuid.UUID) *abandonFixture {
	t.Helper()
	accountID, execID, nodeID := uuid.New(), uuid.New(), uuid.New()

	execNode := *workflow_execution_node.NewWorkflowExecutionNode(execID, nodeID, accountID)
	execNode.Status = status
	if commandID != nil {
		execNode.GraphState = map[string]workflow_execution_node.GraphNodeState{
			"block-1": {Status: workflow_execution_node.GraphNodeRunning, CommandID: commandID},
		}
	}

	execution := workflow_execution.NewWorkflowExecution(uuid.New(), accountID)
	execution.ID = execID
	execution.Status = workflow_execution.StatusRunning

	execRepo := &abandonExecRepo{executions: map[uuid.UUID]*workflow_execution.WorkflowExecution{
		execID: execution,
	}}
	nodeRepo := &abandonExecNodeRepo{
		byNode: map[uuid.UUID][]workflow_execution_node.WorkflowExecutionNode{nodeID: {execNode}},
		byExec: map[uuid.UUID][]workflow_execution_node.WorkflowExecutionNode{execID: {execNode}},
	}
	commands := &abandonCommandService{}

	return &abandonFixture{
		svc: &Service{
			executionRepo:      execRepo,
			executionNodeRepo:  nodeRepo,
			nodeCommandService: commands,
			logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
		execRepo: execRepo, nodeRepo: nodeRepo, commands: commands,
		accountID: accountID, execID: execID, nodeID: nodeID,
	}
}

func (f *abandonFixture) execution(t *testing.T) *workflow_execution.WorkflowExecution {
	t.Helper()
	return f.execRepo.executions[f.execID]
}

// --- tests ----------------------------------------------------------------

// The headline: after the node is gone, completed_at must be stamped — that is
// the field the active-execution cap and the retention sweep both key on.
func TestDeletingANodeFinishesItsRun(t *testing.T) {
	f := newAbandonFixture(t, workflow_execution_node.StatusRunning, nil)

	settled, err := f.svc.AbandonNodeExecutions(context.Background(), f.accountID, f.nodeID)
	if err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled %d execution nodes, want 1", settled)
	}
	if len(f.nodeRepo.updated) != 1 || f.nodeRepo.updated[0].Status != workflow_execution_node.StatusFailed {
		t.Fatalf("execution node was not failed: %+v", f.nodeRepo.updated)
	}
	if f.nodeRepo.updated[0].ErrorMessage != abandonReasonNodeDeleted {
		t.Errorf("reason = %q, want %q", f.nodeRepo.updated[0].ErrorMessage, abandonReasonNodeDeleted)
	}

	execution := f.execution(t)
	if execution.CompletedAt == nil {
		t.Fatal("completed_at is still NULL — the run keeps consuming the account's active cap forever")
	}
}

// A node that was never dispatched (pending, no command) is the same zombie by a
// shorter route, and would not be reached by settling commands alone.
func TestAPendingNodeThatNeverRanIsAlsoSettled(t *testing.T) {
	f := newAbandonFixture(t, workflow_execution_node.StatusPending, nil)

	settled, _ := f.svc.AbandonNodeExecutions(context.Background(), f.accountID, f.nodeID)
	if settled != 1 {
		t.Fatalf("settled %d, want 1", settled)
	}
	if f.execution(t).CompletedAt == nil {
		t.Fatal("completed_at is still NULL for a run that never started")
	}
}

// The device command a running block waits on must be settled too, or it stays
// `registered`/`sent` forever: the queue-expiry sweep only retires QUEUEABLE
// commands and the reconnect settler needs a reconnect that will never come.
func TestTheInFlightCommandIsSettledToo(t *testing.T) {
	commandID := uuid.New()
	f := newAbandonFixture(t, workflow_execution_node.StatusRunning, &commandID)

	if _, err := f.svc.AbandonNodeExecutions(context.Background(), f.accountID, f.nodeID); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	reason, ok := f.commands.failed[commandID]
	if !ok {
		t.Fatal("the block's in-flight command was left unsettled")
	}
	if reason != abandonReasonNodeDeleted {
		t.Errorf("command reason = %q, want %q", reason, abandonReasonNodeDeleted)
	}
}

// A run that already finished must not have its recorded outcome rewritten by a
// later node delete. Refused twice now — the query does not return it, and
// settleExecutionNodes skips it anyway (which is what the cancel path, whose
// reader is unfiltered, still relies on).
func TestATerminalNodeIsLeftAlone(t *testing.T) {
	f := newAbandonFixture(t, workflow_execution_node.StatusCompleted, nil)

	settled, err := f.svc.AbandonNodeExecutions(context.Background(), f.accountID, f.nodeID)
	if err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if settled != 0 {
		t.Errorf("settled %d already-finished nodes, want 0", settled)
	}
	if len(f.nodeRepo.updated) != 0 {
		t.Errorf("a finished execution node was rewritten: %+v", f.nodeRepo.updated)
	}
}

// A node with no runs at all is the common case and must be silent.
func TestDeletingANodeWithNoRunsDoesNothing(t *testing.T) {
	f := newAbandonFixture(t, workflow_execution_node.StatusRunning, nil)

	settled, err := f.svc.AbandonNodeExecutions(context.Background(), f.accountID, uuid.New())
	if err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if settled != 0 {
		t.Errorf("settled %d, want 0", settled)
	}
}

// --- the operator escape hatch --------------------------------------------

func TestCancelSettlesARunningExecution(t *testing.T) {
	commandID := uuid.New()
	f := newAbandonFixture(t, workflow_execution_node.StatusRunning, &commandID)

	settled, err := f.svc.CancelExecution(context.Background(), f.accountID, f.execID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if settled != 1 {
		t.Fatalf("settled %d, want 1", settled)
	}
	if f.execution(t).CompletedAt == nil {
		t.Fatal("a cancelled run is still active")
	}
	if f.commands.failed[commandID] != abandonReasonCancelled {
		t.Errorf("command reason = %q, want %q", f.commands.failed[commandID], abandonReasonCancelled)
	}
}

// The escape hatch must free a run pinned for a reason we did NOT predict — a
// dropped enqueue, a stale finalize — not only the deleted-node case. Here every
// node is already terminal yet completed_at was never stamped.
func TestCancelForceCompletesARunWhoseNodesAreAllTerminal(t *testing.T) {
	f := newAbandonFixture(t, workflow_execution_node.StatusFailed, nil)

	settled, err := f.svc.CancelExecution(context.Background(), f.accountID, f.execID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if settled != 0 {
		t.Errorf("settled %d, want 0 (nothing was non-terminal)", settled)
	}
	if f.execution(t).CompletedAt == nil {
		t.Fatal("a run stuck with all-terminal nodes was not force-completed — the escape hatch does not escape")
	}
}

// Idempotent: a double-click must not rewrite a recorded outcome.
func TestCancellingAFinishedRunIsANoOp(t *testing.T) {
	f := newAbandonFixture(t, workflow_execution_node.StatusCompleted, nil)
	execution := f.execution(t)
	execution.Complete(workflow_execution.StatusCompleted)
	firstCompletion := *execution.CompletedAt
	f.execRepo.updates = 0

	settled, err := f.svc.CancelExecution(context.Background(), f.accountID, f.execID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if settled != 0 {
		t.Errorf("settled %d on a finished run, want 0", settled)
	}
	if f.execRepo.updates != 0 {
		t.Errorf("a finished run was written %d times", f.execRepo.updates)
	}
	if !f.execution(t).CompletedAt.Equal(firstCompletion) {
		t.Error("the original completion timestamp was overwritten")
	}
	if f.execution(t).Status != workflow_execution.StatusCompleted {
		t.Errorf("status = %q, want the original %q", f.execution(t).Status, workflow_execution.StatusCompleted)
	}
}

// Cross-tenant: cancelling another account's run must not resolve at all.
func TestCancelIsAccountScoped(t *testing.T) {
	f := newAbandonFixture(t, workflow_execution_node.StatusRunning, nil)

	if _, err := f.svc.CancelExecution(context.Background(), uuid.New(), f.execID); err == nil {
		t.Fatal("another account's execution was cancellable")
	}
	if f.execution(t).CompletedAt != nil {
		t.Fatal("a cross-tenant cancel settled the run")
	}
}

// A node that finished in the window between the list read and the settle write
// must keep its own outcome — including its outputs.
//
// The settle was a full-row Update, which writes by primary key and honours no
// status condition. A worker committing `completed` in that window was rewritten
// to `failed`, and a capture_image's image_id went with it: the row then named no
// image while the object it captured sat in storage, charged to the account and
// reachable by nothing. The engine's own writes have used the guarded UpdateActive
// since it was introduced; these two settlers were left on the unguarded one.
func TestASettleDoesNotOverwriteANodeThatJustFinished(t *testing.T) {
	commandID := uuid.New()
	f := newAbandonFixture(t, workflow_execution_node.StatusRunning, &commandID)

	// The worker wins the race: the stored row is terminal, with the outputs
	// that make it worth protecting, before the settle reaches its write.
	finished := f.nodeRepo.byNode[f.nodeID][0]
	finished.Status = workflow_execution_node.StatusCompleted
	finished.GraphState = map[string]workflow_execution_node.GraphNodeState{
		"block-1": {
			Status:    workflow_execution_node.GraphNodeCompleted,
			CommandID: &commandID,
			Outputs:   map[string]string{"image_id": "the-capture"},
		},
	}
	f.nodeRepo.updated = append(f.nodeRepo.updated, finished)

	settled, err := f.svc.AbandonNodeExecutions(context.Background(), f.accountID, f.nodeID)
	if err != nil {
		t.Fatalf("AbandonNodeExecutions() = %v, want nil", err)
	}
	if settled != 0 {
		t.Errorf("settled = %d, want 0: the node had already finished on its own", settled)
	}

	last := f.nodeRepo.updated[len(f.nodeRepo.updated)-1]
	if last.Status != workflow_execution_node.StatusCompleted {
		t.Fatalf("stored status = %q, want %q — the settle rewrote a finished node",
			last.Status, workflow_execution_node.StatusCompleted)
	}
	if got := last.GraphState["block-1"].Outputs["image_id"]; got != "the-capture" {
		t.Errorf("outputs lost: image_id = %q, want %q; the captured object is now referenced by nothing", got, "the-capture")
	}
}

// And the command of a node that finished first must NOT be marked failed: the
// old order settled the device command before claiming the row, so a block that
// had just succeeded had its successful command rewritten anyway.
func TestASettleThatLosesTheRaceLeavesTheCommandAlone(t *testing.T) {
	commandID := uuid.New()
	f := newAbandonFixture(t, workflow_execution_node.StatusRunning, &commandID)

	finished := f.nodeRepo.byNode[f.nodeID][0]
	finished.Status = workflow_execution_node.StatusCompleted
	f.nodeRepo.updated = append(f.nodeRepo.updated, finished)

	if _, err := f.svc.AbandonNodeExecutions(context.Background(), f.accountID, f.nodeID); err != nil {
		t.Fatalf("AbandonNodeExecutions() = %v, want nil", err)
	}
	if reason, ok := f.commands.failed[commandID]; ok {
		t.Errorf("the command of an already-finished node was failed with %q; the block's own outcome stands", reason)
	}
}

// A cancelled run's blocks must read as finished in the run view. The settle
// failed the execution node but left the block it was blocked on marked
// running, and nothing else ever looks at a settled row, so the canvas showed
// the step running for good.
func TestCancelLeavesNoBlockRunning(t *testing.T) {
	commandID := uuid.New()
	f := newAbandonFixture(t, workflow_execution_node.StatusRunning, &commandID)

	if _, err := f.svc.CancelExecution(context.Background(), f.accountID, f.execID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(f.nodeRepo.updated) == 0 {
		t.Fatal("the execution node was not written")
	}
	stored := f.nodeRepo.updated[len(f.nodeRepo.updated)-1]
	block := stored.GraphState["block-1"]
	if block.Status != workflow_execution_node.GraphNodeFailed {
		t.Fatalf("block status = %q, want %q", block.Status, workflow_execution_node.GraphNodeFailed)
	}
	if block.Error != abandonReasonCancelled || block.CompletedAt == nil {
		t.Errorf("block = %+v, want the cancel reason and a completion time", block)
	}
	if _, settled := f.commands.failed[commandID]; !settled {
		t.Error("the block's command was not settled")
	}
}
