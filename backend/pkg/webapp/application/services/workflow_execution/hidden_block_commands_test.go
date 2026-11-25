package workflow_execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

type snapshotExecutionRepo struct {
	repository.WorkflowExecutionRepository
	execution *workflow_execution.WorkflowExecution
}

func (r *snapshotExecutionRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*workflow_execution.WorkflowExecution, error) {
	return r.execution, nil
}

// The PRESENCE of a command row answers the question the graph-state redaction
// removes.
//
// RedactGraphStateForGraph omits the state of a secret-fed unprivileged block
// and everything downstream, because `completed` on one branch and `skipped` on
// the other spells the secret out one comparison at a time. Listing the
// downstream block's command answers exactly that — redacting its payload was
// never the point.
func TestCommandsOfHiddenBlocksAreNotListed(t *testing.T) {
	accountID, executionID := uuid.New(), uuid.New()
	conditionCmd, downstreamCmd, unrelatedCmd := uuid.New(), uuid.New(), uuid.New()

	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			// Unprivileged block fed a secret: the seed of the hidden set.
			{ID: "cond", Type: "control.condition", Params: map[string]interface{}{
				"left": "{{secrets.api_key}}", "op": "contains", "right": "a",
			}},
			{ID: "downstream", Type: "command.reboot"},
			{ID: "unrelated", Type: "command.reboot"},
		},
		Edges: []workflow_template.GraphEdge{
			{ID: "e1", From: "start", FromPort: "out", To: "cond"},
			{ID: "e2", From: "cond", FromPort: "true", To: "downstream"},
			{ID: "e3", From: "start", FromPort: "out", To: "unrelated"},
		},
	}

	execNode := wen.WorkflowExecutionNode{
		ID: uuid.New(), ExecutionID: executionID, NodeID: uuid.New(), AccountID: accountID,
		GraphState: map[string]wen.GraphNodeState{
			"cond":       {Status: wen.GraphNodeCompleted, CommandID: &conditionCmd},
			"downstream": {Status: wen.GraphNodeCompleted, CommandID: &downstreamCmd},
			"unrelated":  {Status: wen.GraphNodeCompleted, CommandID: &unrelatedCmd},
		},
	}
	commands := []node_command_domain.NodeCommand{
		{ID: conditionCmd, Slug: node_command_domain.SlugReboot},
		{ID: downstreamCmd, Slug: node_command_domain.SlugReboot},
		{ID: unrelatedCmd, Slug: node_command_domain.SlugReboot},
	}

	svc := &Service{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		executionRepo: &snapshotExecutionRepo{execution: &workflow_execution.WorkflowExecution{
			ID: executionID, AccountID: accountID,
			TemplateSnapshot: &workflow_template.TemplateSnapshot{TemplateID: uuid.New(), Graph: graph},
		}},
	}

	got, err := svc.withoutHiddenBlockCommands(context.Background(), executionID, accountID, &execNode, commands)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}

	listed := map[uuid.UUID]bool{}
	for _, c := range got {
		listed[c.ID] = true
	}
	if listed[downstreamCmd] {
		t.Error("the command of a block downstream of a secret-fed condition is listed; its presence answers whether the comparison matched")
	}
	if listed[conditionCmd] {
		t.Error("the secret-fed condition's own command is listed")
	}
	if !listed[unrelatedCmd] {
		t.Error("an unrelated block's command was hidden; the run history must stay usable")
	}
}

// A graph with no secret-fed condition must lose nothing, or every ordinary run
// pays for this.
func TestAnOrdinaryRunKeepsEveryCommand(t *testing.T) {
	accountID, executionID := uuid.New(), uuid.New()
	cmdID := uuid.New()
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "b1", Type: "command.reboot"},
		},
		Edges: []workflow_template.GraphEdge{{ID: "e1", From: "start", FromPort: "out", To: "b1"}},
	}
	execNode := wen.WorkflowExecutionNode{
		GraphState: map[string]wen.GraphNodeState{"b1": {CommandID: &cmdID}},
	}
	commands := []node_command_domain.NodeCommand{{ID: cmdID, Slug: node_command_domain.SlugReboot}}

	svc := &Service{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		executionRepo: &snapshotExecutionRepo{execution: &workflow_execution.WorkflowExecution{
			ID: executionID, AccountID: accountID,
			TemplateSnapshot: &workflow_template.TemplateSnapshot{TemplateID: uuid.New(), Graph: graph},
		}},
	}

	if got, err := svc.withoutHiddenBlockCommands(context.Background(), executionID, accountID, &execNode, commands); err != nil || len(got) != 1 {
		t.Fatalf("an ordinary run lost commands: %d of 1 left (err %v)", len(got), err)
	}
}

type oneExecNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	node wen.WorkflowExecutionNode
}

func (r *oneExecNodeRepo) GetByExecutionID(context.Context, uuid.UUID) ([]wen.WorkflowExecutionNode, error) {
	return []wen.WorkflowExecutionNode{r.node}, nil
}

type listCommandRepo struct {
	repository.NodeCommandRepository
	commands []node_command_domain.NodeCommand
}

func (r *listCommandRepo) GetByWorkflowExecutionNodeID(context.Context, uuid.UUID) ([]node_command_domain.NodeCommand, error) {
	return r.commands, nil
}

// Through the real entry point, so the permission argument is exercised where a
// caller actually passes it. The helper test above cannot catch a call site that
// hands it the wrong flag.
func TestTheEntryPointHonoursThePermission(t *testing.T) {
	accountID, executionID, nodeID := uuid.New(), uuid.New(), uuid.New()
	condCmd, downstreamCmd := uuid.New(), uuid.New()

	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "cond", Type: "control.condition", Params: map[string]interface{}{
				"left": "{{secrets.api_key}}", "op": "contains", "right": "a",
			}},
			{ID: "downstream", Type: "command.reboot"},
		},
		Edges: []workflow_template.GraphEdge{
			{ID: "e1", From: "start", FromPort: "out", To: "cond"},
			{ID: "e2", From: "cond", FromPort: "true", To: "downstream"},
		},
	}
	execNode := wen.WorkflowExecutionNode{
		ID: uuid.New(), ExecutionID: executionID, NodeID: nodeID, AccountID: accountID,
		GraphState: map[string]wen.GraphNodeState{
			"cond":       {CommandID: &condCmd},
			"downstream": {CommandID: &downstreamCmd},
		},
	}
	svc := &Service{
		logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		executionNodeRepo: &oneExecNodeRepo{node: execNode},
		nodeCommandRepo: &listCommandRepo{commands: []node_command_domain.NodeCommand{
			{ID: condCmd}, {ID: downstreamCmd},
		}},
		executionRepo: &snapshotExecutionRepo{execution: &workflow_execution.WorkflowExecution{
			ID: executionID, AccountID: accountID,
			TemplateSnapshot: &workflow_template.TemplateSnapshot{TemplateID: uuid.New(), Graph: graph},
		}},
	}
	ctx := context.Background()

	withSecrets, err := svc.GetExecutionNodeCommands(ctx, executionID, nodeID, accountID, true)
	if err != nil {
		t.Fatalf("privileged read: %v", err)
	}
	if len(withSecrets) != 2 {
		t.Fatalf("a caller holding secret.read_value saw %d of 2 commands", len(withSecrets))
	}

	withoutSecrets, err := svc.GetExecutionNodeCommands(ctx, executionID, nodeID, accountID, false)
	if err != nil {
		t.Fatalf("unprivileged read: %v", err)
	}
	if len(withoutSecrets) != 0 {
		t.Fatalf("a caller without secret.read_value saw %d commands; every one of them is downstream of a secret-fed condition", len(withoutSecrets))
	}
}

type failingExecutionRepo struct {
	repository.WorkflowExecutionRepository
}

func (failingExecutionRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*workflow_execution.WorkflowExecution, error) {
	return nil, errors.New("db blip")
}

// Hidden commands, half one: the dispatch-to-finalize window. On EVERY pass there is
// an interval where the hidden block's command row exists while no GraphState
// records it, so a poller could catch its presence during any ordinary run of
// a secret-conditioned playbook. The command's own payload block id is what
// closes it.
func TestHiddenCommandInTheDispatchWindowIsNotListed(t *testing.T) {
	accountID, executionID := uuid.New(), uuid.New()
	inWindow := uuid.New()

	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
			{ID: "cond", Type: "control.condition", Params: map[string]interface{}{
				"left": "{{secrets.api_key}}", "op": "contains", "right": "a",
			}},
			{ID: "downstream", Type: "command.reboot"},
		},
		Edges: []workflow_template.GraphEdge{
			{ID: "e1", From: "start", FromPort: "out", To: "cond"},
			{ID: "e2", From: "cond", FromPort: "true", To: "downstream"},
		},
	}
	// The window's exact shape: the command exists, GraphState knows nothing.
	execNode := wen.WorkflowExecutionNode{
		ID: uuid.New(), ExecutionID: executionID, AccountID: accountID,
		GraphState: map[string]wen.GraphNodeState{},
	}
	commands := []node_command_domain.NodeCommand{{
		ID: inWindow, Slug: node_command_domain.SlugReboot,
		Payload: map[string]interface{}{results.WorkflowBlockIDKey: "downstream"},
	}}

	svc := &Service{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		executionRepo: &snapshotExecutionRepo{execution: &workflow_execution.WorkflowExecution{
			ID: executionID, AccountID: accountID,
			TemplateSnapshot: &workflow_template.TemplateSnapshot{TemplateID: uuid.New(), Graph: graph},
		}},
	}

	got, err := svc.withoutHiddenBlockCommands(context.Background(), executionID, accountID, &execNode, commands)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	for _, c := range got {
		if c.ID == inWindow {
			t.Fatal("a hidden block's command was listed during the dispatch window; its presence answers the secret comparison")
		}
	}
}

// Hidden commands, half two: a transient read error must not widen what a caller
// below secret.read_value can see. A visible error the caller retries is
// cheaper than a leak nobody notices.
func TestHiddenCommandFilterFailsClosedOnAReadError(t *testing.T) {
	svc := &Service{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		executionRepo: failingExecutionRepo{},
	}
	execNode := wen.WorkflowExecutionNode{GraphState: map[string]wen.GraphNodeState{}}
	commands := []node_command_domain.NodeCommand{{ID: uuid.New(), Slug: node_command_domain.SlugReboot}}

	got, err := svc.withoutHiddenBlockCommands(context.Background(), uuid.New(), uuid.New(), &execNode, commands)
	if err == nil {
		t.Fatalf("a failed execution read returned %d commands unfiltered; the redaction just disappeared", len(got))
	}
}

// A pre-snapshot run stays unfiltered DELIBERATELY: those runs predate graph
// playbooks entirely, so nothing in them can be secret-fed and there is no
// hidden set to enforce. Failing them closed would 500 every legacy history.
func TestLegacyRunWithoutASnapshotKeepsItsHistory(t *testing.T) {
	accountID, executionID := uuid.New(), uuid.New()
	svc := &Service{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		executionRepo: &snapshotExecutionRepo{execution: &workflow_execution.WorkflowExecution{
			ID: executionID, AccountID: accountID, // no snapshot
		}},
	}
	execNode := wen.WorkflowExecutionNode{GraphState: map[string]wen.GraphNodeState{}}
	commands := []node_command_domain.NodeCommand{{ID: uuid.New(), Slug: node_command_domain.SlugReboot}}

	got, err := svc.withoutHiddenBlockCommands(context.Background(), executionID, accountID, &execNode, commands)
	if err != nil || len(got) != 1 {
		t.Fatalf("legacy run history = %d commands (err %v), want 1 untouched", len(got), err)
	}
}
