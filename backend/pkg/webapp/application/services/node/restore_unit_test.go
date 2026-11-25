package node

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
)

// fakeUnitAssigner answers AssignNodeOU the way the SQL repository does: a unit
// it does not know is ErrOrganizationalUnitNotFound and writes nothing.
type fakeUnitAssigner struct {
	nodes *restoreFakeNodeRepo
	units map[uuid.UUID]bool
	calls int
}

func (f *fakeUnitAssigner) AssignNodeOU(_ context.Context, _, nodeID uuid.UUID, ouID *uuid.UUID) error {
	f.calls++
	if ouID != nil && !f.units[*ouID] {
		return app_errors.ErrOrganizationalUnitNotFound
	}
	n, ok := f.nodes.nodes[nodeID]
	if !ok {
		return app_errors.ErrNodeNotFound
	}
	n.OUID = ouID
	return nil
}

// A deleted node's unit may be deleted while the node is (the unit's delete
// guard counts live nodes only). Restoring that reference verbatim gives the
// node an ou_id pointing at nothing, which every unit-resolving path then
// trips over — HTTP-boot among them, so a network-first device stops booting.
// The restore must drop the unit and bring the node back unassigned.
func TestRestoreDropsAUnitThatNoLongerExists(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	gone := uuid.New()
	nodes.nodes[nodeID].OUID = &gone
	svc.SetUnitAssigner(&fakeUnitAssigner{nodes: nodes, units: map[uuid.UUID]bool{}})

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("restore: %v, want success with the unit dropped", err)
	}
	if restored.OUID != nil {
		t.Errorf("restored node points at unit %s, which no longer exists; want unassigned", *restored.OUID)
	}
	if stored := nodes.nodes[nodeID]; stored == nil || stored.OUID != nil {
		t.Errorf("stored node = %+v, want a row with no unit", stored)
	}
}

// A unit that still exists is given back, through the guarded assignment.
func TestRestoreKeepsAUnitThatStillExists(t *testing.T) {
	svc, nodes, accountID, nodeID := newScopedCleanupService(t)
	unit := uuid.New()
	nodes.nodes[nodeID].OUID = &unit
	assigner := &fakeUnitAssigner{nodes: nodes, units: map[uuid.UUID]bool{unit: true}}
	svc.SetUnitAssigner(assigner)

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, false); err != nil {
		t.Fatalf("delete: %v", err)
	}
	restored, err := svc.RestoreDeleted(context.Background(), accountID, uuid.New(), nodeID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.OUID == nil || *restored.OUID != unit {
		t.Errorf("restored unit = %v, want %s", restored.OUID, unit)
	}
	if assigner.calls != 1 {
		t.Errorf("guarded assignment ran %d times, want 1", assigner.calls)
	}
}
