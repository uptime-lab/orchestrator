package workflow_execution

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

type treeOURepo struct {
	repository.OrganizationalUnitRepository
	ous []organizational_unit.OrganizationalUnit
}

func (r *treeOURepo) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return r.ous, nil
}

// A playbook attached to two units may target the nodes of BOTH, and of their
// children — the union is the whole point of a scope set. Getting this wrong in
// the other direction is worse than it looks: an intersection would let a
// two-unit playbook target nothing at all, and a scheduled one would then skip
// every minute forever with only an "no target nodes" note to show for it.
func TestScopeTargetOUsUnionsTheSetAndItsChildren(t *testing.T) {
	rootA, rootB, childA, childB, unrelated := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	svc := &Service{ouRepo: &treeOURepo{ous: []organizational_unit.OrganizationalUnit{
		{ID: rootA},
		{ID: rootB},
		{ID: childA, ParentID: &rootA},
		{ID: childB, ParentID: &rootB},
		{ID: unrelated},
	}}}

	allowed, err := svc.scopeTargetOUs(context.Background(), uuid.New(), []uuid.UUID{rootA, rootB})
	if err != nil {
		t.Fatalf("scopeTargetOUs() = %v", err)
	}

	for _, want := range []uuid.UUID{rootA, rootB, childA, childB} {
		if !allowed[want] {
			t.Errorf("unit %s is not in the target scope, want it included", want)
		}
	}
	if allowed[unrelated] {
		t.Error("an unrelated unit is in the target scope")
	}
}

// One unit must behave exactly as it did before the set existed.
func TestScopeTargetOUsWithASingleUnitIsUnchanged(t *testing.T) {
	root, child, other := uuid.New(), uuid.New(), uuid.New()
	svc := &Service{ouRepo: &treeOURepo{ous: []organizational_unit.OrganizationalUnit{
		{ID: root}, {ID: child, ParentID: &root}, {ID: other},
	}}}

	allowed, err := svc.scopeTargetOUs(context.Background(), uuid.New(), []uuid.UUID{root})
	if err != nil {
		t.Fatalf("scopeTargetOUs() = %v", err)
	}
	if !allowed[root] || !allowed[child] {
		t.Errorf("allowed = %v, want the unit and its child", allowed)
	}
	if allowed[other] {
		t.Error("an unrelated unit leaked into a single-unit scope")
	}
}

// PrimaryOUID is what pins a run's live-update visibility and what an older
// client reads as the scope. A playbook spanning several units must report
// NONE rather than an arbitrary member: a nil scope reaches account-wide
// holders only, which is narrower, while naming one member would be wrong in
// the widening direction.
func TestPrimaryOUIDRefusesToPickAMemberOfASet(t *testing.T) {
	a, b := uuid.New(), uuid.New()

	if got := (&workflow_template.WorkflowTemplate{OUIDs: []uuid.UUID{a}}).PrimaryOUID(); got == nil || *got != a {
		t.Errorf("PrimaryOUID() = %v, want %s for a single-unit playbook", got, a)
	}
	if got := (&workflow_template.WorkflowTemplate{OUIDs: []uuid.UUID{a, b}}).PrimaryOUID(); got != nil {
		t.Errorf("PrimaryOUID() = %v, want nil for a two-unit playbook", got)
	}
	if got := (&workflow_template.WorkflowTemplate{}).PrimaryOUID(); got != nil {
		t.Errorf("PrimaryOUID() = %v, want nil for an unscoped playbook", got)
	}
}

func TestIsScopedDistinguishesUnscopedFromScoped(t *testing.T) {
	if (&workflow_template.WorkflowTemplate{}).IsScoped() {
		t.Error("an empty scope must read as unscoped — an empty allow-map refuses every node")
	}
	if !(&workflow_template.WorkflowTemplate{OUIDs: []uuid.UUID{uuid.New()}}).IsScoped() {
		t.Error("a one-unit scope must read as scoped")
	}
}
