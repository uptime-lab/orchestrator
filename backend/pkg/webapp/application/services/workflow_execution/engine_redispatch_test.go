package workflow_execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	node_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// firstEntryNodeRepo hands out an execution node in the state a pass that died
// after dispatching leaves behind: pending, with no graph state — which is
// indistinguishable from a run that has not started.
type firstEntryNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	id        uuid.UUID
	accountID uuid.UUID
	execID    uuid.UUID
	written   *wen.WorkflowExecutionNode
}

func (r *firstEntryNodeRepo) GetByID(_ context.Context, id uuid.UUID) (*wen.WorkflowExecutionNode, error) {
	return &wen.WorkflowExecutionNode{
		ID:          id,
		AccountID:   r.accountID,
		ExecutionID: r.execID,
		NodeID:      uuid.New(),
		Status:      wen.StatusPending,
	}, nil
}

func (r *firstEntryNodeRepo) UpdateActive(_ context.Context, n *wen.WorkflowExecutionNode) (bool, error) {
	copied := *n
	r.written = &copied
	return true, nil
}

func (r *firstEntryNodeRepo) GetByExecutionID(_ context.Context, _ uuid.UUID) ([]wen.WorkflowExecutionNode, error) {
	if r.written == nil {
		return nil, nil
	}
	return []wen.WorkflowExecutionNode{*r.written}, nil
}

type runningExecutionRepo struct {
	repository.WorkflowExecutionRepository
	execution *workflow_execution.WorkflowExecution
}

func (r *runningExecutionRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*workflow_execution.WorkflowExecution, error) {
	copied := *r.execution
	return &copied, nil
}

func (r *runningExecutionRepo) Update(_ context.Context, _ *workflow_execution.WorkflowExecution) error {
	return nil
}

type oneNodeRepo struct {
	repository.NodeRepository
}

func (r *oneNodeRepo) GetByID(_ context.Context, _, id uuid.UUID) (*node_domain.Node, error) {
	return &node_domain.Node{ID: id, Name: "lab-01"}, nil
}

// linkedCommandRepo answers the "did an earlier pass already dispatch?" read.
type linkedCommandRepo struct {
	repository.NodeCommandRepository
	commands []node_command_domain.NodeCommand
	err      error
	reads    int
}

// GetByIDForUpdate answers the progress path, which re-reads the command a
// running block is waiting on. Only the mid-run tests reach it.
func (r *linkedCommandRepo) GetByIDForUpdate(_ context.Context, id uuid.UUID) (*node_command_domain.NodeCommand, error) {
	for i := range r.commands {
		if r.commands[i].ID == id {
			copied := r.commands[i]
			return &copied, nil
		}
	}
	return nil, errors.New("no such command")
}

func (r *linkedCommandRepo) GetByWorkflowExecutionNodeID(_ context.Context, _ uuid.UUID) ([]node_command_domain.NodeCommand, error) {
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	return r.commands, nil
}

// redispatchFixture wires a one-agent-block playbook whose execution node is on
// its first entry.
func redispatchFixture(cmdRepo *linkedCommandRepo) (*Service, *firstEntryNodeRepo, *countingNodeService, uuid.UUID) {
	accountID, execID, execNodeID := uuid.New(), uuid.New(), uuid.New()
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "b1", Type: "command." + node_command_domain.SlugReboot},
		},
		Edges: []workflow_template.GraphEdge{{ID: "e1", From: "start", FromPort: "out", To: "b1"}},
	}
	nodeRepo := &firstEntryNodeRepo{id: execNodeID, accountID: accountID, execID: execID}
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

// A live command carrying this execution node's id is proof the first pass
// already dispatched. Re-entering begin() would send a second reboot — and for
// run_script, install_packages, download_file, manage_service or write_file
// nothing else stops it, because device-exclusivity covers only install_os,
// erase_drive and capture_image.
func TestFirstEntryDoesNotRedispatchAfterALostPass(t *testing.T) {
	inFlight := uuid.New()
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{{
		ID:     inFlight,
		Slug:   node_command_domain.SlugReboot,
		Status: node_command_domain.StatusSent,
	}}}
	svc, nodeRepo, nodeSvc, execNodeID := redispatchFixture(cmdRepo)

	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeSvc.dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0: the device already has this command", nodeSvc.dispatched)
	}
	if nodeRepo.written == nil || nodeRepo.written.Status != wen.StatusFailed {
		t.Fatalf("execution node = %v, want failed", nodeRepo.written)
	}
	if !strings.Contains(nodeRepo.written.ErrorMessage, inFlight.String()) {
		t.Errorf("error = %q, want it to name the in-flight command %s",
			nodeRepo.written.ErrorMessage, inFlight)
	}
}

// A settled command is not evidence of anything in flight: the ordinary retry
// path must still work, or one finished run would block the next.
func TestFirstEntryDispatchesWhenTheEarlierCommandIsTerminal(t *testing.T) {
	cmdRepo := &linkedCommandRepo{commands: []node_command_domain.NodeCommand{{
		ID:     uuid.New(),
		Slug:   node_command_domain.SlugReboot,
		Status: node_command_domain.StatusSuperseded,
	}}}
	svc, _, nodeSvc, execNodeID := redispatchFixture(cmdRepo)

	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeSvc.dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", nodeSvc.dispatched)
	}
}

// The ordinary case: nothing was ever dispatched for this execution node.
func TestFirstEntryDispatchesNormally(t *testing.T) {
	cmdRepo := &linkedCommandRepo{}
	svc, _, nodeSvc, execNodeID := redispatchFixture(cmdRepo)

	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if cmdRepo.reads != 1 {
		t.Errorf("prior-dispatch reads = %d, want 1", cmdRepo.reads)
	}
	if nodeSvc.dispatched != 1 {
		t.Fatalf("dispatched = %d, want 1", nodeSvc.dispatched)
	}
}

// Fail CLOSED on a read error: we cannot tell whether a command is already on
// its way, and guessing wrong duplicates it on real hardware.
func TestFirstEntryRefusesToStartWhenThePriorDispatchCannotBeRead(t *testing.T) {
	cmdRepo := &linkedCommandRepo{err: errors.New("connection refused")}
	svc, nodeRepo, nodeSvc, execNodeID := redispatchFixture(cmdRepo)

	svc.processNodeGraph(context.Background(), execNodeID, 1)

	if nodeSvc.dispatched != 0 {
		t.Fatalf("dispatched = %d, want 0", nodeSvc.dispatched)
	}
	if nodeRepo.written == nil || nodeRepo.written.Status != wen.StatusFailed {
		t.Fatalf("execution node = %v, want failed", nodeRepo.written)
	}
}
