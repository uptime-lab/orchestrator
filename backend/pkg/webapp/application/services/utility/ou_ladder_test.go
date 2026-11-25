package utility

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
)

type ladderFakeOUs struct {
	repository.OrganizationalUnitRepository
	rows []organizational_unit.OrganizationalUnit
}

func (f *ladderFakeOUs) GetAllByAccountID(context.Context, uuid.UUID) ([]organizational_unit.OrganizationalUnit, error) {
	return f.rows, nil
}

type ladderFakeNodes struct {
	repository.NodeRepository
	rows []node.Node
}

func (f *ladderFakeNodes) GetAllByAccountID(context.Context, uuid.UUID) ([]node.Node, error) {
	return f.rows, nil
}

type ladderFakeStates struct {
	repository.NodeUtilityStateRepository
	rows  []utility.NodeState
	calls int
}

func (f *ladderFakeStates) ListByNodes(_ context.Context, _ uuid.UUID, _ []uuid.UUID) ([]utility.NodeState, error) {
	f.calls++
	return f.rows, nil
}

// The unit page asks one question — "applied on how many of the nodes this
// reaches" — and the rollup has to answer it over the unit AND its children,
// with the rungs derived the same way the per-node view derives them.
func TestLadderForOUCountsTheRungsAcrossTheSubtree(t *testing.T) {
	accountID := uuid.New()
	parent, child := uuid.New(), uuid.New()
	inParent, inChild, elsewhere := uuid.New(), uuid.New(), uuid.New()
	otherOU := uuid.New()

	states := &ladderFakeStates{rows: []utility.NodeState{
		// Applied: the device confirmed the current desired hash.
		{NodeID: inParent, UtilityID: "docker", DesiredHash: "h1", AppliedHash: "h1"},
		// Update pending: applied once, desired moved since.
		{NodeID: inChild, UtilityID: "docker", DesiredHash: "h2", AppliedHash: "h1"},
		// The third node has no row at all, which is the same fact as never
		// applied — and is what a reimage leaves behind.
	}}
	svc := &Service{
		catalog: &utility.Catalog{Utilities: []utility.Definition{
			{ID: "docker", Name: "Docker"},
		}},
		assignments: &settleFakeAssignments{rows: []utility.Assignment{{
			ID: uuid.New(), AccountID: accountID, UtilityID: "docker",
			AccountWide: true, Enabled: true,
		}}},
		states: states,
		ouRepo: &ladderFakeOUs{rows: []organizational_unit.OrganizationalUnit{
			{ID: parent},
			{ID: child, ParentID: &parent},
			{ID: otherOU},
		}},
		nodeRepo: &ladderFakeNodes{rows: []node.Node{
			{ID: inParent, OUID: &parent},
			{ID: inChild, OUID: &child},
			{ID: elsewhere, OUID: &otherOU},
		}},
		logger: slog.Default(),
	}

	rows, err := svc.LadderForOU(context.Background(), accountID, parent)
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d utilities, want 1", len(rows))
	}
	got := rows[0]
	// Two nodes, not three: the third unit is not under this one.
	if got.Nodes != 2 {
		t.Errorf("Nodes = %d, want the unit's own node plus its child's", got.Nodes)
	}
	if got.Applied != 1 || got.UpdatePending != 1 || got.Declared != 0 || got.Failed != 0 {
		t.Errorf("rungs = applied %d / pending %d / declared %d / failed %d",
			got.Applied, got.UpdatePending, got.Declared, got.Failed)
	}
	// The rungs must account for every node the utility reaches, or the section
	// says "applied on 1 of 2" while a third node is invisible.
	if got.Applied+got.UpdatePending+got.Failed+got.Declared != got.Nodes {
		t.Errorf("the rungs do not sum to Nodes: %+v", got)
	}
	// One query for the whole subtree, not one per node: this is a page load.
	if states.calls != 1 {
		t.Errorf("read the ladder %d times, want one batched read", states.calls)
	}
}

// A node with no stored row has never applied, which is exactly what a reimage
// leaves behind now that install_os forgets the ladder.
func TestLadderForOUCountsAMissingRowAsDeclared(t *testing.T) {
	accountID, ouID, nodeID := uuid.New(), uuid.New(), uuid.New()
	svc := &Service{
		catalog: &utility.Catalog{Utilities: []utility.Definition{{ID: "docker", Name: "Docker"}}},
		assignments: &settleFakeAssignments{rows: []utility.Assignment{{
			ID: uuid.New(), AccountID: accountID, UtilityID: "docker",
			AccountWide: true, Enabled: true,
		}}},
		states:   &ladderFakeStates{},
		ouRepo:   &ladderFakeOUs{rows: []organizational_unit.OrganizationalUnit{{ID: ouID}}},
		nodeRepo: &ladderFakeNodes{rows: []node.Node{{ID: nodeID, OUID: &ouID}}},
		logger:   slog.Default(),
	}

	rows, err := svc.LadderForOU(context.Background(), accountID, ouID)
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(rows) != 1 || rows[0].Declared != 1 || rows[0].Applied != 0 {
		t.Fatalf("want one declared node, got %+v", rows)
	}
}

// An empty unit is a normal state, not an error, and must not reach the
// database with `node_id IN ()`.
func TestLadderForOUIsEmptyForAUnitWithNoNodes(t *testing.T) {
	ouID := uuid.New()
	states := &ladderFakeStates{}
	svc := &Service{
		catalog:     &utility.Catalog{},
		assignments: &settleFakeAssignments{},
		states:      states,
		ouRepo:      &ladderFakeOUs{rows: []organizational_unit.OrganizationalUnit{{ID: ouID}}},
		nodeRepo:    &ladderFakeNodes{},
		logger:      slog.Default(),
	}

	rows, err := svc.LadderForOU(context.Background(), uuid.New(), ouID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("got %v, %v; want an empty rollup and no error", rows, err)
	}
	if states.calls != 0 {
		t.Error("queried the ladder for a unit with no nodes")
	}
}

// The stored rungs describe the world as of the last apply, so an
// assignment edited afterwards kept the unit page green while the node page
// correctly said update_pending — a standing inconsistency, since nothing
// reconciles until an operator applies. The rollup now compares the CURRENT
// pre-resolution hash against the source hash the apply stored.
func TestLadderForOUGoesPendingAfterAnAssignmentEdit(t *testing.T) {
	accountID, ouID, nodeID := uuid.New(), uuid.New(), uuid.New()
	// The assignment as it is NOW (edited since the apply).
	current := utility.Assignment{
		ID: uuid.New(), AccountID: accountID, UtilityID: "docker",
		AccountWide: true, Enabled: true,
		Config: map[string]interface{}{"registry": "edited.example"},
	}
	// The state row as the last apply left it: applied == desired, and the
	// source hash of the config that WAS applied.
	oldSource := utility.HashConfig("docker", map[string]interface{}{"registry": "old.example"})
	states := &ladderFakeStates{rows: []utility.NodeState{{
		NodeID: nodeID, UtilityID: "docker",
		DesiredHash: "resolved-old", AppliedHash: "resolved-old",
		DesiredSourceHash: oldSource,
	}}}
	svc := &Service{
		catalog:     &utility.Catalog{Utilities: []utility.Definition{{ID: "docker", Name: "Docker"}}},
		assignments: &settleFakeAssignments{rows: []utility.Assignment{current}},
		states:      states,
		ouRepo:      &ladderFakeOUs{rows: []organizational_unit.OrganizationalUnit{{ID: ouID}}},
		nodeRepo:    &ladderFakeNodes{rows: []node.Node{{ID: nodeID, OUID: &ouID}}},
		logger:      slog.Default(),
	}

	rows, err := svc.LadderForOU(context.Background(), accountID, ouID)
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(rows) != 1 || rows[0].UpdatePending != 1 || rows[0].Applied != 0 {
		t.Fatalf("after an edit the rollup reads %+v, want 1 update_pending — a green rollup here would be a lie", rows)
	}
}

// A row from before the column existed carries no source hash and must keep
// its stored rung: inventing pending for every legacy row would redden a fleet
// that may be perfectly converged.
func TestLadderForOUKeepsLegacyRowsGreen(t *testing.T) {
	accountID, ouID, nodeID := uuid.New(), uuid.New(), uuid.New()
	current := utility.Assignment{
		ID: uuid.New(), AccountID: accountID, UtilityID: "docker",
		AccountWide: true, Enabled: true,
		Config: map[string]interface{}{"registry": "edited.example"},
	}
	states := &ladderFakeStates{rows: []utility.NodeState{{
		NodeID: nodeID, UtilityID: "docker",
		DesiredHash: "h", AppliedHash: "h", // no source hash
	}}}
	svc := &Service{
		catalog:     &utility.Catalog{Utilities: []utility.Definition{{ID: "docker", Name: "Docker"}}},
		assignments: &settleFakeAssignments{rows: []utility.Assignment{current}},
		states:      states,
		ouRepo:      &ladderFakeOUs{rows: []organizational_unit.OrganizationalUnit{{ID: ouID}}},
		nodeRepo:    &ladderFakeNodes{rows: []node.Node{{ID: nodeID, OUID: &ouID}}},
		logger:      slog.Default(),
	}

	rows, err := svc.LadderForOU(context.Background(), accountID, ouID)
	if err != nil {
		t.Fatalf("rollup: %v", err)
	}
	if len(rows) != 1 || rows[0].Applied != 1 {
		t.Fatalf("legacy row = %+v, want it kept applied until its next apply fills the column", rows)
	}
}
