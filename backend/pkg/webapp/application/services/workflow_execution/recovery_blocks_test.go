package workflow_execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	node_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// recoveryNodeService records the one-shot request rather than performing it.
type recoveryNodeService struct {
	services.NodeService
	calls int
	err   error
}

func (s *recoveryNodeService) RebootToRecovery(_ context.Context, _, _, _ uuid.UUID) error {
	s.calls++
	return s.err
}

// stateNodeRepo answers the poll with whatever the test wants the device to be
// doing at that moment.
type stateNodeRepo struct {
	repository.NodeRepository
	node  *node_domain.Node
	err   error
	reads int
}

func (r *stateNodeRepo) GetByID(_ context.Context, _, id uuid.UUID) (*node_domain.Node, error) {
	r.reads++
	if r.err != nil {
		return nil, r.err
	}
	copied := *r.node
	copied.ID = id
	return &copied, nil
}

func recoveryFixture(nodeSvc services.NodeService, nodeRepo repository.NodeRepository) *engineRun {
	svc := &Service{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		nodeService: nodeSvc,
		nodeRepo:    nodeRepo,
	}
	return &engineRun{
		svc: svc,
		ctx: context.Background(),
		execNode: &wen.WorkflowExecutionNode{
			ID:         uuid.New(),
			AccountID:  uuid.New(),
			NodeID:     uuid.New(),
			Status:     wen.StatusRunning,
			GraphState: map[string]wen.GraphNodeState{},
		},
		graph: &workflow_template.WorkflowGraph{SchemaVersion: workflow_template.GraphSchemaVersion},
	}
}

func recoveryNode(status node_domain.NodeStatus, recovery bool) *node_domain.Node {
	n := &node_domain.Node{Status: status}
	if recovery {
		n.SystemInfo = &node_domain.SystemInfo{
			Hardware: &node_domain.HardwareInfo{
				Boot: &node_domain.BootInfo{RecoveryBootstrap: true, CurrentBootDevice: "http"},
			},
		}
	} else {
		n.SystemInfo = &node_domain.SystemInfo{
			Hardware: &node_domain.HardwareInfo{
				Boot: &node_domain.BootInfo{CurrentBootDevice: "nvme"},
			},
		}
	}
	return n
}

// The block calls the platform's one-shot rather than composing a boot-policy
// change with a reboot: the one-shot is what remembers the previous target and
// puts the node back, so a run that dies mid-capture does not strand a device
// in recovery.
func TestRebootToRecoveryUsesTheOneShotService(t *testing.T) {
	nodeSvc := &recoveryNodeService{}
	run := recoveryFixture(nodeSvc, &stateNodeRepo{node: recoveryNode(node_domain.StatusOnline, false)})

	run.execRebootToRecovery("b1", workflow_template.GraphNode{
		ID: "b1", Type: workflow_block.TypeRebootToRecovery,
	})

	if nodeSvc.calls != 1 {
		t.Fatalf("RebootToRecovery calls = %d, want 1", nodeSvc.calls)
	}
	if got := run.execNode.GraphState["b1"].Status; got != wen.GraphNodeCompleted {
		t.Errorf("block status = %q, want completed", got)
	}
}

func TestRebootToRecoveryFailsTheBlockWhenTheServiceRefuses(t *testing.T) {
	nodeSvc := &recoveryNodeService{err: errors.New("node is a watcher")}
	run := recoveryFixture(nodeSvc, &stateNodeRepo{node: recoveryNode(node_domain.StatusOnline, false)})

	run.execRebootToRecovery("b1", workflow_template.GraphNode{
		ID: "b1", Type: workflow_block.TypeRebootToRecovery,
	})

	if got := run.execNode.GraphState["b1"].Status; got != wen.GraphNodeFailed {
		t.Errorf("block status = %q, want failed", got)
	}
}

// The wait must settle immediately when the node is already where the playbook
// wants it — a run that idles 15 seconds to discover that reads as broken.
func TestWaitForNodeCompletesImmediatelyWhenAlreadyInState(t *testing.T) {
	repo := &stateNodeRepo{node: recoveryNode(node_domain.StatusOnline, true)}
	run := recoveryFixture(&recoveryNodeService{}, repo)

	run.execWaitForNode("b1", workflow_template.GraphNode{
		ID: "b1", Type: workflow_block.TypeWaitForNode,
		Params: map[string]interface{}{"state": "recovery", "timeout_seconds": 900},
	})

	if got := run.execNode.GraphState["b1"].Status; got != wen.GraphNodeCompleted {
		t.Fatalf("block status = %q, want completed", got)
	}
	if repo.reads != 1 {
		t.Errorf("node reads = %d, want 1", repo.reads)
	}
}

// ...and must keep waiting while it is not, without settling either way.
func TestWaitForNodeKeepsWaitingWhileTheStateIsWrong(t *testing.T) {
	repo := &stateNodeRepo{node: recoveryNode(node_domain.StatusOnline, false)}
	run := recoveryFixture(&recoveryNodeService{}, repo)

	run.execWaitForNode("b1", workflow_template.GraphNode{
		ID: "b1", Type: workflow_block.TypeWaitForNode,
		Params: map[string]interface{}{"state": "recovery", "timeout_seconds": 900},
	})

	st := run.execNode.GraphState["b1"]
	if st.Status == wen.GraphNodeCompleted || st.Status == wen.GraphNodeFailed {
		t.Errorf("block status = %q, want it still running", st.Status)
	}
	if st.WaitUntil == nil {
		t.Error("no deadline recorded — a restart would wait forever")
	}
}

// A device that never comes back must be REPORTED. Silence is the failure mode
// this block exists to remove.
func TestWaitForNodeFailsAtTheDeadline(t *testing.T) {
	repo := &stateNodeRepo{node: recoveryNode(node_domain.StatusOffline, false)}
	run := recoveryFixture(&recoveryNodeService{}, repo)
	past := time.Now().Add(-time.Minute)
	run.execNode.GraphState["b1"] = wen.GraphNodeState{
		Status: wen.GraphNodeRunning, WaitUntil: &past,
	}

	run.progressWaitForNode("b1", workflow_template.GraphNode{
		ID: "b1", Type: workflow_block.TypeWaitForNode,
		Params: map[string]interface{}{"state": "recovery"},
	}, run.execNode.GraphState["b1"])

	st := run.execNode.GraphState["b1"]
	if st.Status != wen.GraphNodeFailed {
		t.Fatalf("block status = %q, want failed", st.Status)
	}
	if !strings.Contains(st.Error, "recovery") {
		t.Errorf("error = %q, want it to name the state that was waited for", st.Error)
	}
}

// managed_os is "online and NOT the recovery bootstrap", never a boot-device
// test: the same bootstrap reports http when netbooted and sd from a
// provisioning card, so the medium cannot separate an OS from an initramfs.
func TestWaitForNodeManagedOSIgnoresTheBootMedium(t *testing.T) {
	// Recovery bootstrap that booted from an SD card — reports "sd", and is
	// emphatically not a managed OS.
	sdRecovery := &node_domain.Node{
		Status: node_domain.StatusOnline,
		SystemInfo: &node_domain.SystemInfo{Hardware: &node_domain.HardwareInfo{
			Boot: &node_domain.BootInfo{RecoveryBootstrap: true, CurrentBootDevice: "sd"},
		}},
	}
	run := recoveryFixture(&recoveryNodeService{}, &stateNodeRepo{node: sdRecovery})

	run.execWaitForNode("b1", workflow_template.GraphNode{
		ID: "b1", Type: workflow_block.TypeWaitForNode,
		Params: map[string]interface{}{"state": "managed_os", "timeout_seconds": 900},
	})

	if got := run.execNode.GraphState["b1"].Status; got == wen.GraphNodeCompleted {
		t.Error("a recovery bootstrap booted from SD was accepted as a managed OS")
	}
}

// A read failure says nothing about the device, so it must not end the wait —
// only the deadline does.
func TestWaitForNodeSurvivesAReadFailure(t *testing.T) {
	repo := &stateNodeRepo{err: errors.New("connection refused")}
	run := recoveryFixture(&recoveryNodeService{}, repo)

	run.execWaitForNode("b1", workflow_template.GraphNode{
		ID: "b1", Type: workflow_block.TypeWaitForNode,
		Params: map[string]interface{}{"state": "online", "timeout_seconds": 900},
	})

	if got := run.execNode.GraphState["b1"].Status; got == wen.GraphNodeFailed {
		t.Error("a database blip failed the wait; only the deadline should")
	}
}
