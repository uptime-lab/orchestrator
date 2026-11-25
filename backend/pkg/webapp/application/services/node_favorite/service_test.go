package node_favorite

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

type fakeFavorites struct {
	repository.NodeFavoriteRepository
	marks map[uuid.UUID]bool
}

func (f *fakeFavorites) Set(_ context.Context, _, nodeID uuid.UUID, favorite bool) error {
	if favorite {
		f.marks[nodeID] = true
	} else {
		delete(f.marks, nodeID)
	}
	return nil
}

type fakeNodes struct {
	repository.NodeRepository
	nodes map[uuid.UUID]*node.Node
}

func (f fakeNodes) GetByID(_ context.Context, accountID, id uuid.UUID) (*node.Node, error) {
	if n, ok := f.nodes[id]; ok && n.AccountID == accountID {
		return n, nil
	}
	return nil, app_errors.ErrNodeNotFound
}

// unitReader grants node.read in exactly one unit.
type unitReader struct{ unit uuid.UUID }

func (u unitReader) Can(_ context.Context, _, _ uuid.UUID, action authz.Action, ou *uuid.UUID) bool {
	return action == authz.ActionNodeRead && ou != nil && *ou == u.unit
}

// A member who may read one unit must get the same answer for a node in
// another unit as for a node that does not exist; otherwise the favourite
// write is an existence oracle for ids across units.
func TestSetFavoriteAnswersNotFoundOutsideTheCallersUnits(t *testing.T) {
	accountID, userID := uuid.New(), uuid.New()
	unitA, unitB := uuid.New(), uuid.New()
	inA := &node.Node{ID: uuid.New(), AccountID: accountID, OUID: &unitA}
	inB := &node.Node{ID: uuid.New(), AccountID: accountID, OUID: &unitB}
	unassigned := &node.Node{ID: uuid.New(), AccountID: accountID}
	favs := &fakeFavorites{marks: map[uuid.UUID]bool{}}
	svc := NewService(favs, fakeNodes{nodes: map[uuid.UUID]*node.Node{inA.ID: inA, inB.ID: inB, unassigned.ID: unassigned}}, slog.Default())
	svc.SetReadAuthorizer(unitReader{unit: unitA})

	if err := svc.SetFavorite(context.Background(), accountID, userID, inA.ID, true); err != nil {
		t.Fatalf("a node in the caller's unit: %v", err)
	}
	for name, id := range map[string]uuid.UUID{"other unit": inB.ID, "unassigned": unassigned.ID, "unknown": uuid.New()} {
		if err := svc.SetFavorite(context.Background(), accountID, userID, id, true); !errors.Is(err, app_errors.ErrNodeNotFound) {
			t.Errorf("%s: got %v, want ErrNodeNotFound", name, err)
		}
		if favs.marks[id] {
			t.Errorf("%s: the mark was stored", name)
		}
	}
}

// Removing a mark reveals nothing: it answers alike for any id, so a member
// who lost a unit can still clear the marks they made there.
func TestUnsetFavoriteIsAllowedForAnyID(t *testing.T) {
	accountID, userID, unitB := uuid.New(), uuid.New(), uuid.New()
	inB := &node.Node{ID: uuid.New(), AccountID: accountID, OUID: &unitB}
	favs := &fakeFavorites{marks: map[uuid.UUID]bool{inB.ID: true}}
	svc := NewService(favs, fakeNodes{nodes: map[uuid.UUID]*node.Node{inB.ID: inB}}, slog.Default())
	svc.SetReadAuthorizer(unitReader{unit: uuid.New()})

	for _, id := range []uuid.UUID{inB.ID, uuid.New()} {
		if err := svc.SetFavorite(context.Background(), accountID, userID, id, false); err != nil {
			t.Errorf("unset %s: %v", id, err)
		}
	}
	if favs.marks[inB.ID] {
		t.Error("the stale mark was not cleared")
	}
}

// With no authorizer wired the write refuses rather than skipping the check.
func TestSetFavoriteFailsClosedWithoutAnAuthorizer(t *testing.T) {
	accountID := uuid.New()
	n := &node.Node{ID: uuid.New(), AccountID: accountID}
	svc := NewService(&fakeFavorites{marks: map[uuid.UUID]bool{}}, fakeNodes{nodes: map[uuid.UUID]*node.Node{n.ID: n}}, slog.Default())
	if err := svc.SetFavorite(context.Background(), accountID, uuid.New(), n.ID, true); err == nil {
		t.Fatal("expected a refusal with no authorizer wired")
	}
}
