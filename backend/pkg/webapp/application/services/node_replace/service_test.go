package node_replace

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	workflow_execution_service "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	secret_domain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	we "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	wen "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// nodes is the node repository both services read; AssignNode moves a node
// in it, so the run's own scope check sees the move.
type nodes struct {
	repository.NodeRepository
	byID map[uuid.UUID]*node.Node
}

func (r *nodes) GetByID(_ context.Context, _, id uuid.UUID) (*node.Node, error) {
	if n, ok := r.byID[id]; ok {
		cp := *n
		return &cp, nil
	}
	return nil, app_errors.ErrNodeNotFound
}

type attributes struct {
	source   uuid.UUID
	upserted int
	deleted  int
}

func (a *attributes) ListNodeAttributes(_ context.Context, _, nodeID uuid.UUID) ([]secret_domain.NodeAttribute, error) {
	if nodeID != a.source {
		return nil, nil
	}
	return []secret_domain.NodeAttribute{{Key: "ci_token", Value: "v", Sensitive: true}}, nil
}

func (a *attributes) UpsertNodeAttribute(context.Context, audit.Actor, uuid.UUID, uuid.UUID, string, string, bool) (*secret_domain.NodeAttribute, error) {
	a.upserted++
	return &secret_domain.NodeAttribute{}, nil
}

func (a *attributes) DeleteNodeAttribute(context.Context, audit.Actor, uuid.UUID, uuid.UUID, string) error {
	a.deleted++
	return nil
}

type units struct {
	nodes    *nodes
	assigned int
}

func (u *units) AssignNode(_ context.Context, _ audit.Actor, _, nodeID uuid.UUID, ouID *uuid.UUID) error {
	u.assigned++
	u.nodes.byID[nodeID].OUID = ouID
	return nil
}

type templates struct {
	repository.WorkflowTemplateRepository
	tpl *workflow_template.WorkflowTemplate
}

func (r templates) GetByID(context.Context, uuid.UUID, uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	if r.tpl == nil {
		return nil, app_errors.ErrNotFound
	}
	return r.tpl, nil
}

type executions struct {
	repository.WorkflowExecutionRepository
	active  int64
	created int
}

func (r *executions) CountActiveByAccountID(context.Context, uuid.UUID) (int64, error) {
	return r.active, nil
}

func (r *executions) Create(context.Context, *we.WorkflowExecution) error {
	r.created++
	return nil
}

func (r *executions) UpdateNodeCount(context.Context, uuid.UUID, uuid.UUID, int) error { return nil }

type executionNodes struct {
	repository.WorkflowExecutionNodeRepository
}

func (executionNodes) Create(context.Context, *wen.WorkflowExecutionNode) error { return nil }

func (executionNodes) CountActiveByAccountID(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}

type unitTree struct {
	repository.OrganizationalUnitRepository
}

func (unitTree) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return nil, nil
}

type replaceFixture struct {
	svc            *Service
	auth           commands.AuthContext
	source, target uuid.UUID
	tpl            *workflow_template.WorkflowTemplate
	attrs          *attributes
	units          *units
	execs          *executions
	sink           *audittest.Sink
}

// newReplaceFixture: the source sits in a unit, the target in another, and
// the playbook is scoped to the source's unit.
func newReplaceFixture(t *testing.T, cfg config.WorkflowConfig) *replaceFixture {
	t.Helper()
	accountID, userID := uuid.New(), uuid.New()
	sourceOU, targetOU := uuid.New(), uuid.New()
	source, target := uuid.New(), uuid.New()
	repo := &nodes{byID: map[uuid.UUID]*node.Node{
		source: {ID: source, AccountID: accountID, Hostname: "builder-1", OUID: &sourceOU},
		target: {ID: target, AccountID: accountID, OUID: &targetOU},
	}}
	tpl := &workflow_template.WorkflowTemplate{
		ID: uuid.New(), AccountID: accountID, Name: "re-register",
		Status: workflow_template.StatusActive, OUIDs: []uuid.UUID{sourceOU},
	}
	execs := &executions{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runs := workflow_execution_service.NewService(execs, executionNodes{}, templates{tpl: tpl}, repo,
		nil, unitTree{}, nil, nil, nil, cfg, logger)
	recorder, sink := audittest.New()
	runs.SetAuditRecorder(recorder)
	attrs := &attributes{source: source}
	u := &units{nodes: repo}
	return &replaceFixture{
		svc:    &Service{nodeRepo: repo, secrets: attrs, ous: u, workflows: runs, logger: logger},
		auth:   commands.NewAuthContext(accountID, userID),
		source: source, target: target, tpl: tpl,
		attrs: attrs, units: u, execs: execs, sink: sink,
	}
}

func (f *replaceFixture) nothingMoved(t *testing.T) {
	t.Helper()
	if f.attrs.upserted != 0 || f.units.assigned != 0 || f.attrs.deleted != 0 || f.execs.created != 0 {
		t.Fatalf("a refused replace changed state: upserts %d, unit moves %d, strips %d, runs %d",
			f.attrs.upserted, f.units.assigned, f.attrs.deleted, f.execs.created)
	}
}

// A playbook start that will be refused is refused BEFORE the identity moves,
// with a typed error, so the two nodes never both hold it.
func TestReplaceRefusesAnUnstartablePlaybookBeforeMovingTheIdentity(t *testing.T) {
	t.Run("draft", func(t *testing.T) {
		f := newReplaceFixture(t, config.WorkflowConfig{})
		f.tpl.Status = workflow_template.StatusDraft
		_, err := f.svc.Replace(context.Background(), f.auth, f.source, f.target, &f.tpl.ID)
		if !errors.Is(err, ErrPlaybookRefused) || !errors.Is(err, app_errors.ErrWorkflowTemplateNotActive) {
			t.Fatalf("Replace = %v, want ErrPlaybookRefused wrapping ErrWorkflowTemplateNotActive", err)
		}
		f.nothingMoved(t)
	})
	t.Run("run cap", func(t *testing.T) {
		f := newReplaceFixture(t, config.WorkflowConfig{MaxActiveExecutionsPerAccount: 1})
		f.execs.active = 1
		_, err := f.svc.Replace(context.Background(), f.auth, f.source, f.target, &f.tpl.ID)
		if !errors.Is(err, ErrPlaybookRefused) || !errors.Is(err, app_errors.ErrTooManyActiveExecutions) {
			t.Fatalf("Replace = %v, want ErrPlaybookRefused wrapping ErrTooManyActiveExecutions", err)
		}
		f.nothingMoved(t)
	})
}

// The scope test is asked with the target in the unit it is about to join,
// and the run it starts records its start under the member who replaced.
func TestReplaceStartsTheRunUnderTheMember(t *testing.T) {
	f := newReplaceFixture(t, config.WorkflowConfig{})
	res, err := f.svc.Replace(context.Background(), f.auth, f.source, f.target, &f.tpl.ID)
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if res.ExecutionID == nil || f.execs.created != 1 {
		t.Fatal("no run was started")
	}
	start, ok := f.sink.Find(audit_event.ActionPlaybookRunStart)
	if !ok {
		t.Fatalf("no %s row; actions = %v", audit_event.ActionPlaybookRunStart, f.sink.Actions())
	}
	if start.ActorUserID == nil || *start.ActorUserID != f.auth.UserID || start.TargetID != res.ExecutionID.String() {
		t.Fatalf("run start row = %+v, want the member against execution %s", start, res.ExecutionID)
	}
}
