package workflow_execution

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// timeoutCmdRepo answers the two calls the timeout path makes. Everything else
// panics through the embedded nil interface — reaching it means the test's
// assumptions about the path are wrong.
type timeoutCmdRepo struct {
	repository.NodeCommandRepository
	cmd *node_command_domain.NodeCommand
	// takeBackCalls records UpdateStatusIfCurrent invocations.
	takeBackCalls int
	takeBackOK    bool
	gotNewStatus  node_command_domain.NodeCommandStatus
}

func (f *timeoutCmdRepo) GetByIDForUpdate(_ context.Context, _ uuid.UUID) (*node_command_domain.NodeCommand, error) {
	return f.cmd, nil
}

func (f *timeoutCmdRepo) UpdateStatusIfCurrent(_ context.Context, _ uuid.UUID, _, newStatus node_command_domain.NodeCommandStatus, _ int, _ string) (bool, error) {
	f.takeBackCalls++
	f.gotNewStatus = newStatus
	return f.takeBackOK, nil
}

func timedOutAgentRun(cmdStatus node_command_domain.NodeCommandStatus, retries int, takeBackOK bool) (*engineRun, *timeoutCmdRepo, uuid.UUID) {
	cmdID := uuid.New()
	repo := &timeoutCmdRepo{
		cmd:        &node_command_domain.NodeCommand{ID: cmdID, Status: cmdStatus},
		takeBackOK: takeBackOK,
	}
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "reboot", Type: "command.reboot",
				Policy: &workflow_template.BlockPolicy{TimeoutSeconds: 1, Retries: retries}},
		},
	}
	svc := liveEngineService()
	svc.nodeCommandRepo = repo
	started := time.Now().Add(-30 * time.Second) // long past the 1s timeout
	r := &engineRun{
		svc:   svc,
		ctx:   context.Background(),
		graph: graph,
		execNode: &wen.WorkflowExecutionNode{GraphState: map[string]wen.GraphNodeState{
			"reboot": {Status: wen.GraphNodeRunning, StartedAt: &started, CommandID: &cmdID, Attempts: 1},
		}},
	}
	return r, repo, cmdID
}

// A block timeout on a command that was never sent must take the command back:
// a queued reboot delivered hours after its run already failed is not "late",
// it is a different event happening to a machine whose operator was told the
// run is over.
func TestBlockTimeoutTakesBackAQueuedCommand(t *testing.T) {
	r, repo, _ := timedOutAgentRun(node_command_domain.StatusRegistered, 0, true)
	node, _ := r.graph.NodeByID("reboot")

	r.progressAgentBlock("reboot", node, r.execNode.GraphState["reboot"])

	if repo.takeBackCalls != 1 {
		t.Fatalf("UpdateStatusIfCurrent calls = %d, want 1", repo.takeBackCalls)
	}
	if repo.gotNewStatus != node_command_domain.StatusExpired {
		t.Errorf("taken-back status = %q, want %q", repo.gotNewStatus, node_command_domain.StatusExpired)
	}
	st := r.execNode.GraphState["reboot"]
	if st.Status != wen.GraphNodeFailed {
		t.Errorf("block status = %q, want failed", st.Status)
	}
	if !strings.Contains(st.Error, "taken back") {
		t.Errorf("block error must say the command was taken back: %q", st.Error)
	}
}

// A timeout on a command already on the device cannot cancel it — and it must
// NOT retry either: a retry dispatches a second copy of a command that is
// still running, which for the disk-writing slugs is two writers on one
// device. The retry budget (5 here) is deliberately not honored.
func TestBlockTimeoutDoesNotRetryWhileCommandRuns(t *testing.T) {
	r, repo, _ := timedOutAgentRun(node_command_domain.StatusInProgress, 5, false)
	node, _ := r.graph.NodeByID("reboot")

	r.progressAgentBlock("reboot", node, r.execNode.GraphState["reboot"])

	if repo.takeBackCalls != 0 {
		t.Fatalf("an in-progress command must not be taken back (calls = %d)", repo.takeBackCalls)
	}
	st := r.execNode.GraphState["reboot"]
	if st.Status != wen.GraphNodeFailed {
		t.Fatalf("block status = %q, want failed (a retry would have re-dispatched)", st.Status)
	}
	if st.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 — the retry policy must not fire while the command runs", st.Attempts)
	}
	if !strings.Contains(st.Error, "retries are not attempted") {
		t.Errorf("block error must state the retry decision: %q", st.Error)
	}
}

// The conditional take-back can LOSE the race: the dispatcher sent the command
// between our read and the write. The refusal must then behave exactly like
// the in-flight case — no retry.
func TestBlockTimeoutRespectsALostTakeBackRace(t *testing.T) {
	r, repo, _ := timedOutAgentRun(node_command_domain.StatusRegistered, 5, false /* take-back loses */)
	node, _ := r.graph.NodeByID("reboot")

	r.progressAgentBlock("reboot", node, r.execNode.GraphState["reboot"])

	if repo.takeBackCalls != 1 {
		t.Fatalf("take-back should have been attempted once (calls = %d)", repo.takeBackCalls)
	}
	st := r.execNode.GraphState["reboot"]
	if st.Status != wen.GraphNodeFailed || st.Attempts != 1 {
		t.Errorf("lost race must settle without retry: status=%q attempts=%d", st.Status, st.Attempts)
	}
}
