package workflow

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/authz/authztest"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	workflow_template_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_template"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	we "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

type runTemplateRepo struct {
	repository.WorkflowTemplateRepository
	tpl *workflow_template.WorkflowTemplate
}

func (r runTemplateRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	return r.tpl, nil
}

type runExecRepo struct {
	repository.WorkflowExecutionRepository
	stored *we.WorkflowExecution
}

func (r *runExecRepo) Create(_ context.Context, e *we.WorkflowExecution) error {
	cp := *e
	r.stored = &cp
	return nil
}

func (r *runExecRepo) UpdateNodeCount(context.Context, uuid.UUID, uuid.UUID, int) error { return nil }

func (r *runExecRepo) GetByID(context.Context, uuid.UUID, uuid.UUID) (*we.WorkflowExecution, error) {
	if r.stored == nil {
		return nil, app_errors.ErrNotFound
	}
	cp := *r.stored
	return &cp, nil
}

type runExecNodeRepo struct {
	repository.WorkflowExecutionNodeRepository
}

func (runExecNodeRepo) Create(context.Context, *wen.WorkflowExecutionNode) error { return nil }

type runNodeRepo struct {
	repository.NodeRepository
	nodes map[uuid.UUID]bool
}

func (r runNodeRepo) GetByID(_ context.Context, accountID, nodeID uuid.UUID) (*node.Node, error) {
	if r.nodes[nodeID] {
		return &node.Node{ID: nodeID, AccountID: accountID}, nil
	}
	return nil, app_errors.ErrNodeNotFound
}

func runHandler(t *testing.T, accountID, userID uuid.UUID, known uuid.UUID, execs *runExecRepo) (*echo.Echo, *audittest.Sink, *workflow_template.WorkflowTemplate) {
	t.Helper()
	recorder, sink := audittest.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tpl := &workflow_template.WorkflowTemplate{ID: uuid.New(), AccountID: accountID, Name: "p", Status: workflow_template.StatusActive}
	execSvc := workflow_execution_service.NewService(
		execs, runExecNodeRepo{}, runTemplateRepo{tpl: tpl}, runNodeRepo{nodes: map[uuid.UUID]bool{known: true}},
		nil, nil, nil, nil, nil, config.WorkflowConfig{}, logger,
	)
	execSvc.SetAuditRecorder(recorder)
	tplSvc := workflow_template_service.NewService(runTemplateRepo{tpl: tpl}, recorder, logger)
	h := NewWorkflowHandler(tplSvc, execSvc,
		authztest.ActiveMemberWithRole(accountID, userID, authz.RoleSuperAdmin), recorder, logger)

	e := echo.New()
	auth := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set("userID", userID.String())
			c.Set("accountID", accountID.String())
			return next(c)
		}
	}
	e.POST("/api/v1/workflow-executions", h.StartExecution, auth)
	e.POST("/api/v1/workflow-executions/:id/cancel", h.CancelExecution, auth)
	return e, sink, tpl
}

func postJSON(e *echo.Echo, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// The engine dispatches a run's commands as the platform, so starting and
// cancelling a run must each leave a row naming the member who did it.
func TestRunStartAndCancelAreAuditedUnderTheMember(t *testing.T) {
	accountID, userID, nodeID := uuid.New(), uuid.New(), uuid.New()
	execs := &runExecRepo{}
	e, sink, tpl := runHandler(t, accountID, userID, nodeID, execs)

	rec := postJSON(e, "/api/v1/workflow-executions",
		`{"template_id":"`+tpl.ID.String()+`","node_ids":["`+nodeID.String()+`"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start status = %d: %s", rec.Code, rec.Body.String())
	}
	start, ok := sink.Find(audit_event.ActionPlaybookRunStart)
	if !ok {
		t.Fatalf("no %s row; actions = %v", audit_event.ActionPlaybookRunStart, sink.Actions())
	}
	if start.ActorUserID == nil || *start.ActorUserID != userID {
		t.Fatalf("start actor = %v, want the member %s", start.ActorUserID, userID)
	}
	if start.TargetType != audit_event.TargetPlaybookExecution || start.TargetID != execs.stored.ID.String() {
		t.Fatalf("start target = %s/%s, want the execution %s", start.TargetType, start.TargetID, execs.stored.ID)
	}

	// A finished run: the cancel settles nothing but is still the member's act.
	now := time.Now()
	execs.stored.CompletedAt = &now
	rec = postJSON(e, "/api/v1/workflow-executions/"+execs.stored.ID.String()+"/cancel", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d: %s", rec.Code, rec.Body.String())
	}
	cancel, ok := sink.Find(audit_event.ActionPlaybookRunCancel)
	if !ok {
		t.Fatalf("no %s row; actions = %v", audit_event.ActionPlaybookRunCancel, sink.Actions())
	}
	if cancel.ActorUserID == nil || *cancel.ActorUserID != userID || cancel.TargetID != execs.stored.ID.String() {
		t.Fatalf("cancel row = %+v, want the member against the execution", cancel)
	}
}

// One target outside the account refuses the whole run with 404 — the same
// answer for another account's node and for an id that does not exist — and
// records no run start.
func TestRunWithAnUnknownTargetIsRefusedWhole(t *testing.T) {
	accountID, userID, nodeID := uuid.New(), uuid.New(), uuid.New()
	execs := &runExecRepo{}
	e, sink, tpl := runHandler(t, accountID, userID, nodeID, execs)

	rec := postJSON(e, "/api/v1/workflow-executions",
		`{"template_id":"`+tpl.ID.String()+`","node_ids":["`+nodeID.String()+`","`+uuid.NewString()+`"]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if execs.stored != nil {
		t.Fatal("a refused run created an execution")
	}
	if _, ok := sink.Find(audit_event.ActionPlaybookRunStart); ok {
		t.Fatal("a refused run recorded a run start")
	}
}

// An unknown execution records no cancel: the trail must not claim an act
// that reached nothing.
func TestCancelOfAnUnknownRunIsNotAudited(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	e, sink, _ := runHandler(t, accountID, userID, uuid.New(), &runExecRepo{})

	rec := postJSON(e, "/api/v1/workflow-executions/"+uuid.NewString()+"/cancel", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := sink.Actions(); len(got) != 0 {
		t.Fatalf("recorded %v for a cancel that reached nothing", got)
	}
}
