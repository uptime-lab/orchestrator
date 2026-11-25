package workflow

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	node_command_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_command"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
)

// The secret.values_read event was once written at permission-check time,
// so a request for a nonexistent execution node recorded "plaintext served"
// and then got a 404. An audit row claiming a read that never happened is the
// one lie the trail must not tell — the row now follows the read.

type timingExecNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
	nodes []wen.WorkflowExecutionNode
}

func (r *timingExecNodeRepo) GetByExecutionID(context.Context, uuid.UUID) ([]wen.WorkflowExecutionNode, error) {
	return r.nodes, nil
}

type timingCmdRepo struct {
	repository.NodeCommandRepository
}

func (timingCmdRepo) GetByWorkflowExecutionNodeID(context.Context, uuid.UUID) ([]node_command_domain.NodeCommand, error) {
	return nil, nil
}

func timingHandler(t *testing.T, accountID, userID uuid.UUID, nodes []wen.WorkflowExecutionNode) (*echo.Echo, *audittest.Sink) {
	t.Helper()
	recorder, sink := audittest.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	execSvc := workflow_execution_service.NewService(
		nil, &timingExecNodeRepo{nodes: nodes}, nil, nil, timingCmdRepo{}, nil, nil, nil, nil,
		config.WorkflowConfig{}, logger,
	)
	h := NewWorkflowHandler(nil, execSvc,
		authztest.ActiveMemberWithRole(accountID, userID, authz.RoleSuperAdmin), recorder, logger)

	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", userID.String())
			c.Set("accountID", accountID.String())
			return next(c)
		}
	}
	e.GET("/api/v1/workflow-executions/:id/nodes/:nodeId/commands", h.GetExecutionNodeCommands, auth)
	return e, sink
}

func TestNo404LeavesASecretReadAuditRow(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	e, sink := timingHandler(t, accountID, userID, nil) // no execution nodes at all

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/workflow-executions/"+uuid.NewString()+"/nodes/"+uuid.NewString()+"/commands", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a nonexistent execution node", rec.Code)
	}
	if got := len(sink.Events()); got != 0 {
		t.Fatalf("%d audit row(s) written for a read that answered 404 — the trail claims plaintext was served when nothing was", got)
	}
}

func TestASuccessfulReadStillRecords(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	executionID, nodeID := uuid.New(), uuid.New()
	e, sink := timingHandler(t, accountID, userID, []wen.WorkflowExecutionNode{{
		ID: uuid.New(), ExecutionID: executionID, NodeID: nodeID, AccountID: accountID,
	}})

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/workflow-executions/"+executionID.String()+"/nodes/"+nodeID.String()+"/commands", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := len(sink.Events()); got != 1 {
		t.Fatalf("%d audit row(s), want exactly one for a served privileged read", got)
	}
}
