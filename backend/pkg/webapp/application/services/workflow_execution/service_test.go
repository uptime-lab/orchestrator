package workflow_execution

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/workflow_execution_start"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
	"github.com/google/uuid"
)

// stubTemplateRepo returns a fixed template from GetByID; the draft check runs
// before any other dependency is touched, so the rest can stay nil.
type stubTemplateRepo struct {
	template *workflow_template.WorkflowTemplate
}

func (s *stubTemplateRepo) Create(ctx context.Context, t *workflow_template.WorkflowTemplate) error {
	return errors.New("not implemented")
}

func (s *stubTemplateRepo) Update(ctx context.Context, t *workflow_template.WorkflowTemplate) error {
	return errors.New("not implemented")
}

func (s *stubTemplateRepo) DeleteScopeByOUID(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

func (s *stubTemplateRepo) UpdateOUIDs(ctx context.Context, accountID, templateID uuid.UUID, ouIDs []uuid.UUID, authorID *uuid.UUID) error {
	return errors.New("not implemented")
}

func (s *stubTemplateRepo) GetByID(ctx context.Context, accountID, templateID uuid.UUID) (*workflow_template.WorkflowTemplate, error) {
	if s.template == nil {
		return nil, errors.New("not found")
	}
	return s.template, nil
}

func (s *stubTemplateRepo) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]workflow_template.WorkflowTemplate, error) {
	return nil, errors.New("not implemented")
}

func (s *stubTemplateRepo) GetAllByAccountIDWithFilters(ctx context.Context, accountID uuid.UUID, name *string, isShortcut *bool) ([]workflow_template.WorkflowTemplate, error) {
	return nil, errors.New("not implemented")
}

func (s *stubTemplateRepo) Delete(ctx context.Context, accountID, templateID uuid.UUID) error {
	return errors.New("not implemented")
}

func (s *stubTemplateRepo) ListScheduledCandidates(ctx context.Context, perAccountLimit int) ([]workflow_template.ScheduledCandidate, error) {
	return nil, errors.New("not implemented")
}

func (s *stubTemplateRepo) GetByIDs(ctx context.Context, ids []uuid.UUID) ([]workflow_template.WorkflowTemplate, error) {
	return nil, errors.New("not implemented")
}

func (s *stubTemplateRepo) CountByOUID(ctx context.Context, accountID, ouID uuid.UUID) (int64, error) {
	return 0, errors.New("not implemented")
}

func TestStartExecutionRejectsDraftTemplate(t *testing.T) {
	accountID := uuid.New()
	template := &workflow_template.WorkflowTemplate{
		ID:        uuid.New(),
		AccountID: accountID,
		Name:      "draft playbook",
		Status:    workflow_template.StatusDraft,
	}

	svc := &Service{
		templateRepo: &stubTemplateRepo{template: template},
		logger:       slog.Default(),
	}

	cmd := workflow_execution_start.WorkflowExecutionStartCommand{
		TemplateID:  template.ID,
		NodeIDs:     []uuid.UUID{uuid.New()},
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	}

	_, err := svc.StartExecution(context.Background(), cmd)
	if !errors.Is(err, app_errors.ErrWorkflowTemplateNotActive) {
		t.Fatalf("expected ErrWorkflowTemplateNotActive, got %v", err)
	}
}

// Scope-check stubs: embed the interface so only the methods the scope check
// touches need implementations; an unexpected call panics via the nil
// embedded interface.

type stubScopeNodeRepo struct {
	repository.NodeRepository
	nodes map[uuid.UUID]*node.Node
}

func (s *stubScopeNodeRepo) GetByID(ctx context.Context, accountID, nodeID uuid.UUID) (*node.Node, error) {
	if n, ok := s.nodes[nodeID]; ok {
		return n, nil
	}
	return nil, app_errors.ErrNodeNotFound
}

type stubScopeOURepo struct {
	repository.OrganizationalUnitRepository
	ous []organizational_unit.OrganizationalUnit
}

func (s *stubScopeOURepo) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return s.ous, nil
}

// errCreateSentinel marks that StartExecution got past the scope check: the
// execution-repo Create stub fails with it, stopping the test before the
// engine machinery (which is not wired) is reached.
var errCreateSentinel = errors.New("create sentinel")

type stubScopeExecutionRepo struct {
	repository.WorkflowExecutionRepository
}

func (s *stubScopeExecutionRepo) Create(ctx context.Context, e *workflow_execution.WorkflowExecution) error {
	return errCreateSentinel
}

// TestStartExecutionOUScope verifies the run-time scope rule matches the run
// picker's availability rule: a playbook scoped to an OU may target nodes in
// that OU or one of its children, and nothing else.
func TestStartExecutionOUScope(t *testing.T) {
	accountID := uuid.New()
	rootOU := uuid.New()
	childOU := uuid.New()
	otherOU := uuid.New()

	ous := []organizational_unit.OrganizationalUnit{
		{ID: rootOU, AccountID: accountID},
		{ID: childOU, AccountID: accountID, ParentID: &rootOU},
		{ID: otherOU, AccountID: accountID},
	}

	template := &workflow_template.WorkflowTemplate{
		ID:        uuid.New(),
		AccountID: accountID,
		Name:      "scoped playbook",
		Status:    workflow_template.StatusActive,
		OUIDs:     []uuid.UUID{rootOU},
	}

	cases := []struct {
		name      string
		nodeOU    *uuid.UUID
		forbidden bool
	}{
		{"node in the playbook's own OU", &rootOU, false},
		{"node in a child of the playbook's OU", &childOU, false},
		{"node in an unrelated OU", &otherOU, true},
		{"node with no OU", nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodeID := uuid.New()
			svc := &Service{
				templateRepo:  &stubTemplateRepo{template: template},
				nodeRepo:      &stubScopeNodeRepo{nodes: map[uuid.UUID]*node.Node{nodeID: {ID: nodeID, AccountID: accountID, OUID: tc.nodeOU}}},
				ouRepo:        &stubScopeOURepo{ous: ous},
				executionRepo: &stubScopeExecutionRepo{},
				logger:        slog.Default(),
			}

			cmd := workflow_execution_start.WorkflowExecutionStartCommand{
				TemplateID:  template.ID,
				NodeIDs:     []uuid.UUID{nodeID},
				AuthContext: commands.NewAuthContext(accountID, uuid.New()),
			}

			_, err := svc.StartExecution(context.Background(), cmd)
			if tc.forbidden {
				if !errors.Is(err, app_errors.ErrForbidden) {
					t.Fatalf("expected ErrForbidden, got %v", err)
				}
			} else if !errors.Is(err, errCreateSentinel) {
				t.Fatalf("expected to pass the scope check and hit the create sentinel, got %v", err)
			}
		})
	}
}
