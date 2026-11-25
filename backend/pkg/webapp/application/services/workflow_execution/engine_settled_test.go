package workflow_execution

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	node_command_cmd "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_command"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// storedNodeRepo answers the re-read a pass makes before it starts a block. The
// stored row is what a settler (cancel, or a node delete) has already written —
// deliberately NOT the copy the pass is holding, because the whole question is
// whether the two still agree.
type storedNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	status wen.WorkflowExecutionNodeStatus
	gone   bool
	err    error
	reads  int
	writes int
}

func (r *storedNodeRepo) GetByID(_ context.Context, id uuid.UUID) (*wen.WorkflowExecutionNode, error) {
	r.reads++
	switch {
	case r.err != nil:
		return nil, r.err
	case r.gone:
		return nil, app_errors.ErrNotFound
	}
	return &wen.WorkflowExecutionNode{ID: id, Status: r.status}, nil
}

func (r *storedNodeRepo) UpdateActive(_ context.Context, _ *wen.WorkflowExecutionNode) (bool, error) {
	r.writes++
	return true, nil
}

// countingNodeService records every device command the engine dispatches. The
// point is that this count stays at zero once a run is
// cancelled — a discarded database write does not un-send an erase_drive.
type countingNodeService struct {
	services.NodeService
	dispatched int
}

func (s *countingNodeService) SendBatchCommand(
	_ context.Context,
	_ node_command_cmd.BatchNodeCommandCommand,
) (*node_command_cmd.BatchCommandResult, error) {
	s.dispatched++
	id := uuid.New()
	return &node_command_cmd.BatchCommandResult{
		Results: []node_command_cmd.NodeCommandResultItem{{CommandID: &id}},
	}, nil
}

// settledFixture is a one-block graph whose single block is an AGENT block, so
// starting it has an effect on real hardware.
func settledFixture(repo *storedNodeRepo) (*engineRun, *countingNodeService, workflow_template.GraphNode) {
	node := workflow_template.GraphNode{ID: "reboot", Type: "command." + node_command_domain.SlugReboot}
	nodeSvc := &countingNodeService{}
	svc := &Service{
		logger:            slog.Default(),
		executionNodeRepo: repo,
		nodeService:       nodeSvc,
	}
	r := &engineRun{
		svc: svc,
		ctx: context.Background(),
		execNode: &wen.WorkflowExecutionNode{
			ID: uuid.New(), AccountID: uuid.New(), NodeID: uuid.New(),
			Status:     wen.StatusRunning,
			GraphState: map[string]wen.GraphNodeState{},
		},
		graph: &workflow_template.WorkflowGraph{Nodes: []workflow_template.GraphNode{node}},
	}
	return r, nodeSvc, node
}

// A pass parked in a 600 s http_request holds a
// pre-cancel copy of the row. UpdateActive stops it resurrecting the run, but
// nothing stopped it dispatching one more device command first.
func TestStartBlockRefusesToDispatchForASettledRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		repo  *storedNodeRepo
		block bool
	}{
		{"cancelled", &storedNodeRepo{status: wen.StatusFailed}, true},
		{"completed", &storedNodeRepo{status: wen.StatusCompleted}, true},
		{"node row gone", &storedNodeRepo{gone: true}, true},
		{"still running", &storedNodeRepo{status: wen.StatusRunning}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, nodeSvc, node := settledFixture(tc.repo)

			r.startBlock("reboot", node)

			if tc.block {
				if nodeSvc.dispatched != 0 {
					t.Errorf("dispatched %d device commands for a settled run, want 0", nodeSvc.dispatched)
				}
				if !r.abandoned {
					t.Error("the pass did not mark itself abandoned")
				}
				if got := r.execNode.GraphState["reboot"].Status; got != "" {
					t.Errorf("block state = %q, want it left untouched", got)
				}
			} else {
				if nodeSvc.dispatched != 1 {
					t.Errorf("dispatched %d device commands for a live run, want 1", nodeSvc.dispatched)
				}
				if r.abandoned {
					t.Error("a live run was treated as abandoned")
				}
			}
		})
	}
}

// A database blip must not silently stop every run in flight. It is also not
// evidence of a cancel — and the same blip will fail the finalize write anyway,
// which is where a real problem surfaces.
func TestAReadFailureDoesNotStopTheRun(t *testing.T) {
	repo := &storedNodeRepo{err: errors.New("connection reset")}
	r, nodeSvc, node := settledFixture(repo)

	r.startBlock("reboot", node)

	if nodeSvc.dispatched != 1 {
		t.Errorf("dispatched %d device commands, want 1 — the guard must fail open", nodeSvc.dispatched)
	}
	if r.abandoned {
		t.Error("a read error was mistaken for a cancel")
	}
}

// An abandoned pass writes NOTHING. UpdateActive would refuse the write anyway,
// but going through it would log a concurrency warning for something that is not
// a race — the settler owns this run's outcome and has already recorded it.
func TestFinalizeWritesNothingForAnAbandonedPass(t *testing.T) {
	repo := &storedNodeRepo{status: wen.StatusFailed}
	r, _, node := settledFixture(repo)

	r.startBlock("reboot", node)
	r.finalize(0)

	if repo.writes != 0 {
		t.Errorf("an abandoned pass wrote %d times, want 0", repo.writes)
	}
}
