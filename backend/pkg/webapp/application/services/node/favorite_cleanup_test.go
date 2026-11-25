package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// Favorites are the one node-scoped leftover no account-scoped query can
// surface: the rows key on node_id alone. Node ids are reused on purpose (the
// replace flow keeps the logical node across a hardware swap), so a surviving
// row does not stay inert — it re-attaches the old owner's star to whatever
// device next answers to that id.

type fakeFavoriteRepo struct {
	deleted []uuid.UUID
	err     error
}

func (f *fakeFavoriteRepo) ListNodeIDsByUser(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeFavoriteRepo) Set(context.Context, uuid.UUID, uuid.UUID, bool) error { return nil }

func (f *fakeFavoriteRepo) DeleteByNode(_ context.Context, nodeID uuid.UUID) error {
	f.deleted = append(f.deleted, nodeID)
	return f.err
}

func newDeleteService(t *testing.T, favorites *fakeFavoriteRepo) (*Service, uuid.UUID, uuid.UUID) {
	t.Helper()
	accountID, nodeID := uuid.New(), uuid.New()
	repo := &deleteFakeRepo{nodes: map[uuid.UUID]*node.Node{
		nodeID: {ID: nodeID, AccountID: accountID, Hostname: "rpi-abcd0001", Status: node.StatusOnline},
	}}
	recorder, _ := audittest.New()
	svc := &Service{
		nodeRepo:     repo,
		favoriteRepo: favorites,
		audit:        recorder,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return svc, accountID, nodeID
}

func TestDeleteNodeClearsFavorites(t *testing.T) {
	favorites := &fakeFavoriteRepo{}
	svc, accountID, nodeID := newDeleteService(t, favorites)

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if len(favorites.deleted) != 1 || favorites.deleted[0] != nodeID {
		t.Fatalf("favorites cleared for %v, want exactly [%v]", favorites.deleted, nodeID)
	}
}

// The cleanup is best-effort for the same reason every sibling cleanup is: the
// node row is already gone, so failing here would leave the caller with a
// deleted node and an error, and no way to retry the part that failed.
func TestDeleteNodeSurvivesFavoriteCleanupFailure(t *testing.T) {
	favorites := &fakeFavoriteRepo{err: errors.New("favorites table unavailable")}
	svc, accountID, nodeID := newDeleteService(t, favorites)

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete must not fail because a favorite row could not be cleared: %v", err)
	}
	if len(favorites.deleted) != 1 {
		t.Fatalf("cleanup was not attempted: %v", favorites.deleted)
	}
}

// A service with nothing wired must still delete. Every other optional cleanup
// on this path is nil-guarded and this one has to match, or an install that
// never calls the setter panics on the most destructive call in the API.
func TestDeleteNodeWithoutFavoriteRepo(t *testing.T) {
	svc, accountID, nodeID := newDeleteService(t, nil)
	svc.favoriteRepo = nil

	if err := svc.Delete(context.Background(), accountID, uuid.New(), nodeID, true); err != nil {
		t.Fatalf("delete with no favorite repo wired: %v", err)
	}
}
