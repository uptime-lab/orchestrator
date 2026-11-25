package node

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	authz_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodecommand "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
	we "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

type listedCommands struct {
	services.NodeCommandService
	cmds []nodecommand.NodeCommand
}

func (s listedCommands) GetNodeCommandsByNodeID(context.Context, uuid.UUID, uuid.UUID, int, int) ([]nodecommand.NodeCommand, error) {
	return s.cmds, nil
}

func (s listedCommands) GetNodeCommandByID(_ context.Context, _, id uuid.UUID) (*nodecommand.NodeCommand, error) {
	for i := range s.cmds {
		if s.cmds[i].ID == id {
			return &s.cmds[i], nil
		}
	}
	return nil, app_errors.ErrNodeCommandNotFound
}

type oneStepRepo struct {
	repository.WorkflowExecutionNodeRepository
	step wen.WorkflowExecutionNode
}

func (r oneStepRepo) GetByID(context.Context, uuid.UUID) (*wen.WorkflowExecutionNode, error) {
	cp := r.step
	return &cp, nil
}

type oneRunRepo struct {
	repository.WorkflowExecutionRepository
	run *we.WorkflowExecution
}

func (r oneRunRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*we.WorkflowExecution, error) {
	return r.run, nil
}

// A node's command list and a single command are a second read path to a
// run's commands: a member without secret.read_value must not see there the
// command of a block downstream of a secret-fed condition, which the run's
// own view omits because its presence answers the comparison.
func TestCommandReadsOmitHiddenRunCommandsBelowSecretRead(t *testing.T) {
	accountID, userID, nodeID := uuid.New(), uuid.New(), uuid.New()
	executionID, stepID := uuid.New(), uuid.New()
	hiddenCmd, plainCmd, directCmd := uuid.New(), uuid.New(), uuid.New()
	graph := &workflow_template.WorkflowGraph{
		SchemaVersion: workflow_template.GraphSchemaVersion,
		Nodes: []workflow_template.GraphNode{
			{ID: "start", Type: workflow_block.TypeStart},
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
	step := wen.WorkflowExecutionNode{
		ID: stepID, ExecutionID: executionID, NodeID: nodeID, AccountID: accountID,
		GraphState: map[string]wen.GraphNodeState{
			"downstream": {Status: wen.GraphNodeCompleted, CommandID: &hiddenCmd},
			"unrelated":  {Status: wen.GraphNodeCompleted, CommandID: &plainCmd},
		},
	}
	cmds := []nodecommand.NodeCommand{
		{ID: hiddenCmd, NodeID: nodeID, AccountID: accountID, Slug: "reboot", WorkflowExecutionNodeID: &stepID},
		{ID: plainCmd, NodeID: nodeID, AccountID: accountID, Slug: "reboot", WorkflowExecutionNodeID: &stepID},
		{ID: directCmd, NodeID: nodeID, AccountID: accountID, Slug: "reboot"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runs := workflow_execution_service.NewService(
		oneRunRepo{run: &we.WorkflowExecution{ID: executionID, AccountID: accountID,
			TemplateSnapshot: &workflow_template.TemplateSnapshot{TemplateID: uuid.New(), Graph: graph}}},
		oneStepRepo{step: step}, nil, nil, nil, nil, nil, nil, nil, config.WorkflowConfig{}, logger)

	handlerFor := func(role authz.Role) *NodeHandler {
		recorder, _ := audittest.New()
		enforcer := authz_service.NewEnforcer(
			batchMemberRepo{member: account_member.NewAccountMember(accountID, userID)},
			batchBindingRepo{bindings: []role_binding.RoleBinding{
				*role_binding.NewRoleBinding(accountID, userID, role, nil, nil),
			}},
			batchRoleSettingRepo{}, batchOURepo{}, recorder, logger,
		)
		h := &NodeHandler{
			nodeService:        &batchCountingNodeService{},
			nodeCommandService: listedCommands{cmds: cmds},
			enforcer:           enforcer,
			audit:              recorder,
			logger:             logger,
		}
		h.SetRunCommandFilter(runs)
		return h
	}
	get := func(h *NodeHandler, fn echo.HandlerFunc, id uuid.UUID) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
		c.SetParamNames("id")
		c.SetParamValues(id.String())
		c.Set("userID", userID.String())
		c.Set("accountID", accountID.String())
		if err := fn(c); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	listed := func(rec *httptest.ResponseRecorder) map[string]bool {
		var body struct {
			Data struct {
				Commands []struct {
					ID string `json:"id"`
				} `json:"commands"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding %s: %v", rec.Body.String(), err)
		}
		out := map[string]bool{}
		for _, c := range body.Data.Commands {
			out[c.ID] = true
		}
		return out
	}

	viewer := handlerFor(authz.RoleViewer)
	got := listed(get(viewer, viewer.GetNodeCommands, nodeID))
	if got[hiddenCmd.String()] {
		t.Error("the node's command list showed a hidden block's command to a member without secret.read_value")
	}
	if !got[plainCmd.String()] || !got[directCmd.String()] {
		t.Errorf("an unrelated or direct command was dropped: %v", got)
	}
	if rec := get(viewer, viewer.GetNodeCommand, hiddenCmd); rec.Code != http.StatusNotFound {
		t.Errorf("reading the hidden command by id answered %d, want 404", rec.Code)
	}

	admin := handlerFor(authz.RoleAdmin)
	if got := listed(get(admin, admin.GetNodeCommands, nodeID)); !got[hiddenCmd.String()] {
		t.Error("a secret.read_value holder lost the hidden command")
	}
}
