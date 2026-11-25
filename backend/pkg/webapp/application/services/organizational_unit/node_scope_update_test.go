package organizational_unit

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
)

// recordingScopeUpdater records the live-stream scope refreshes an assignment
// produces.
type recordingScopeUpdater struct {
	nodes []string
	ous   []*uuid.UUID
}

func (u *recordingScopeUpdater) UpdateNodeOU(nodeID string, ouID *uuid.UUID) {
	u.nodes = append(u.nodes, nodeID)
	u.ous = append(u.ous, ouID)
}

// The gRPC registry captures a node's unit once at connect, and the metrics push
// scopes itself by that copy. Writing the column alone left a connected node
// feeding its OLD unit's viewers and starving the new one until it reconnected,
// so the assignment must repoint the live connection.
func TestAssignNodeRepointsTheLiveConnectionScope(t *testing.T) {
	updater := &recordingScopeUpdater{}
	svc, nodeID := pushTestService(nil)
	svc.SetNodeScopeUpdater(updater)

	ouID := uuid.New()
	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, &ouID); err != nil {
		t.Fatalf("AssignNode: %v", err)
	}
	if len(updater.nodes) != 1 || updater.nodes[0] != nodeID.String() {
		t.Fatalf("scope updated for %v, want exactly [%s]", updater.nodes, nodeID)
	}
	if updater.ous[0] == nil || *updater.ous[0] != ouID {
		t.Errorf("scope updated to %v, want %s", updater.ous[0], ouID)
	}

	// Clearing the unit is the same move in the other direction.
	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, nil); err != nil {
		t.Fatalf("AssignNode(nil): %v", err)
	}
	if len(updater.ous) != 2 || updater.ous[1] != nil {
		t.Errorf("clearing the unit updated the scope to %v, want nil", updater.ous)
	}
}

// A failed assignment must leave the live scope alone: the column still says
// the old unit, and a stream scoped to a unit the node is not in is the leak
// in the other direction.
func TestAFailedAssignmentDoesNotRepointTheScope(t *testing.T) {
	updater := &recordingScopeUpdater{}
	svc, nodeID := pushTestService(nil)
	svc.SetNodeScopeUpdater(updater)
	svc.ouMutationRepo = &assignFakeMutationRepo{err: errors.New("unit is gone")}

	ouID := uuid.New()
	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, &ouID); err == nil {
		t.Fatal("expected the assignment to fail")
	}
	if len(updater.nodes) != 0 {
		t.Errorf("repointed the scope of %v for a failed assignment, want none", updater.nodes)
	}
}

// No updater wired keeps working — the pre-existing behaviour, and what an
// install that has not been rewired relies on.
func TestNoScopeUpdaterIsNotAFailure(t *testing.T) {
	svc, nodeID := pushTestService(nil)

	ouID := uuid.New()
	if err := svc.AssignNode(context.Background(), audit.SystemActor(uuid.New()), uuid.New(), nodeID, &ouID); err != nil {
		t.Errorf("AssignNode without a scope updater: %v", err)
	}
}
