package workflow_execution

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// midRunNodeRepo hands out an execution node in an arbitrary mid-run state, so a
// test can express "block A finished, block B's dispatch was lost".
type midRunNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	accountID uuid.UUID
	execID    uuid.UUID
	status    wen.WorkflowExecutionNodeStatus
	state     map[string]wen.GraphNodeState
	written   *wen.WorkflowExecutionNode
}

func (r *midRunNodeRepo) GetByID(_ context.Context, id uuid.UUID) (*wen.WorkflowExecutionNode, error) {
	state := map[string]wen.GraphNodeState{}
	for k, v := range r.state {
		state[k] = v
	}
	return &wen.WorkflowExecutionNode{
		ID:          id,
		AccountID:   r.accountID,
		ExecutionID: r.execID,
		NodeID:      uuid.New(),
		Status:      r.status,
		GraphState:  state,
	}, nil
}

func (r *midRunNodeRepo) UpdateActive(_ context.Context, n *wen.WorkflowExecutionNode) (bool, error) {
	copied := *n
	r.written = &copied
	return true, nil
}

func (r *midRunNodeRepo) GetByExecutionID(_ context.Context, _ uuid.UUID) ([]wen.WorkflowExecutionNode, error) {
	if r.written == nil {
		return nil, nil
	}
	return []wen.WorkflowExecutionNode{*r.written}, nil
}

// midRunFixture wires a two-agent-block chain: start -> b1 -> b2.
func midRunFixture(cmdRepo *linkedCommandRepo, status wen.WorkflowExecutionNodeStatus,
	state map[string]wen.GraphNodeState) (*Service, *midRunNodeRepo, *countingNodeService, uuid.UUID) {
	accountID, execID, execNodeID := uuid.New(), uuid.New(), uuid.New()
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "b1", Type: "command." + node_command_domain.SlugReboot},
			{ID: "b2", Type: "command." + node_command_domain.SlugReboot},
		},
		Edges: []workflow_template.GraphEdge{
			{ID: "e1", From: "start", FromPort: "out", To: "b1"},
			{ID: "e2", From: "b1", FromPort: "out", To: "b2"},
		},
	}
	nodeRepo := &midRunNodeRepo{accountID: accountID, execID: execID, status: status, state: state}
	execRepo := &runningExecutionRepo{execution: &workflow_execution.WorkflowExecution{
		ID:        execID,
		AccountID: accountID,
		Status:    workflow_execution.StatusRunning,
		TemplateSnapshot: &workflow_template.TemplateSnapshot{
			TemplateID: uuid.New(),
			Name:       "nightly",
			Graph:      graph,
		},
	}}
	nodeSvc := &countingNodeService{}
	svc := &Service{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		executionNodeRepo: nodeRepo,
		executionRepo:     execRepo,
		nodeRepo:          &oneNodeRepo{},
		nodeCommandRepo:   cmdRepo,
		nodeService:       nodeSvc,
	}
	return svc, nodeRepo, nodeSvc, execNodeID
}

// The duplicate-dispatch guard has to run on EVERY pass, not just the first.
//
// It was gated on "pending with no graph state", which is the run's FIRST block
// alone. Mid-run the shape is identical and the consequence is the same: block
// b1 settles, the pass dispatches b2 and dies before finalize persists it, the
// stored state still says b1 is what happened, and the next enqueue takes the
// progress() branch and sends b2 a SECOND time. For reboot, run_script,
// install_packages, download_file, manage_service or write_file nothing else
// catches it — device-exclusivity covers only install_os, erase_drive and
// capture_image.
func TestMidRunDoesNotRedispatchAfterALostPass(t *testing.T) {
	recorded, lost := uuid.New(), uuid.New()
	startedAt := time.Now().Add(-time.Minute)
	// The exact residue of a lost pass: the stored state still says b1 is
	// RUNNING, because the pass that saw b1's command succeed, marked b1
	// completed and dispatched b2 died before finalize persisted any of it.
	state := map[string]wen.GraphNodeState{
		"start": {Status: wen.GraphNodeCompleted},
		"b1":    {Status: wen.GraphNodeRunning, CommandID: &recorded, StartedAt: &startedAt},
	}
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{
		// b1's command settled — which is what makes the next pass advance.
		{ID: recorded, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSuccess},
		// b2's dispatch, live on the device and recorded in no graph state.
		{ID: lost, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSent},
	}}

	svc, nodeRepo, nodeSvc, execNodeID := midRunFixture(cmdRepo, wen.StatusRunning, state)
	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeSvc.dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0: the device already has this command, and a duplicate reboot is a real one", nodeSvc.dispatched)
	}
	if nodeRepo.written == nil || nodeRepo.written.Status != wen.StatusFailed {
		t.Fatalf("execution node = %v, want failed", nodeRepo.written)
	}
	if !strings.Contains(nodeRepo.written.ErrorMessage, lost.String()) {
		t.Errorf("error = %q, want it to name the in-flight command %s", nodeRepo.written.ErrorMessage, lost)
	}
}

// The ordinary mid-run case must not trip the guard: a block that IS running has
// a live command, and its id is recorded in the graph state. Treating that as
// evidence of a lost pass would fail every run with a step in flight — which is
// most of them.
func TestMidRunWithARecordedRunningCommandIsNotTreatedAsALostPass(t *testing.T) {
	running := uuid.New()
	startedAt := time.Now().Add(-time.Minute)
	state := map[string]wen.GraphNodeState{
		"start": {Status: wen.GraphNodeCompleted},
		"b1":    {Status: wen.GraphNodeRunning, CommandID: &running, StartedAt: &startedAt},
	}
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{
		{ID: running, Slug: node_command_domain.SlugReboot, Status: node_command_domain.StatusSent},
	}}

	svc, nodeRepo, nodeSvc, execNodeID := midRunFixture(cmdRepo, wen.StatusRunning, state)
	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeSvc.dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0: b1 is still running, so there is nothing to advance", nodeSvc.dispatched)
	}
	if nodeRepo.written != nil && nodeRepo.written.Status == wen.StatusFailed {
		t.Fatalf("the run was failed for having a command in flight: %q", nodeRepo.written.ErrorMessage)
	}
}
