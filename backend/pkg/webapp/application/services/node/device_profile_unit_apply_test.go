package node

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

type unitApplyRepo struct {
	repository.NodeRepository
	nodes []node.Node
}

func (r *unitApplyRepo) GetAllByAccountID(context.Context, uuid.UUID) ([]node.Node, error) {
	return r.nodes, nil
}

func (r *unitApplyRepo) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*node.Node, error) {
	for i := range r.nodes {
		if r.nodes[i].ID == id {
			return &r.nodes[i], nil
		}
	}
	return nil, nil
}

// The fleet apply reaches exactly the unit's DIRECT members, and a per-node
// refusal lands in Skipped instead of aborting the sweep. The per-node apply
// itself is exercised elsewhere; here its deps are deliberately unset, so
// every member refuses — which is precisely the property under test: two
// members, two skips, the outsider untouched, no error escaping.
func TestUnitApplySweepsMembersAndCollectsRefusals(t *testing.T) {
	unit := uuid.New()
	other := uuid.New()
	inA := node.Node{ID: uuid.New(), Name: "a", OUID: &unit}
	inB := node.Node{ID: uuid.New(), Name: "b", OUID: &unit}
	out := node.Node{ID: uuid.New(), Name: "c", OUID: &other}
	unassigned := node.Node{ID: uuid.New(), Name: "d"}

	s := &Service{
		nodeRepo: &unitApplyRepo{nodes: []node.Node{inA, inB, out, unassigned}},
		logger:   slog.Default(),
	}

	report, err := s.ApplyDeviceProfileToUnit(context.Background(), uuid.New(), uuid.New(), unit)
	if err != nil {
		t.Fatalf("sweep returned an error, want per-node refusals collected: %v", err)
	}
	if report.Dispatched != 0 || report.Queued != 0 {
		t.Errorf("dispatched=%d queued=%d, want 0/0 with deps unset", report.Dispatched, report.Queued)
	}
	if len(report.Skipped) != 2 {
		t.Fatalf("skipped %d nodes, want exactly the unit's 2 members: %+v", len(report.Skipped), report.Skipped)
	}
	names := map[string]bool{report.Skipped[0].Name: true, report.Skipped[1].Name: true}
	if !names["a"] || !names["b"] {
		t.Errorf("skipped %v, want members a and b", names)
	}
	for _, sk := range report.Skipped {
		if sk.Reason == "" {
			t.Errorf("skip for %s carries no reason", sk.Name)
		}
	}
}
