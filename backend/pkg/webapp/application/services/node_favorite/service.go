package node_favorite

import (
	"context"
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/google/uuid"
)

// Service implements per-user node favorites: a personal view over the
// account's nodes with no policy semantics.
type Service struct {
	favoriteRepo repository.NodeFavoriteRepository
	nodeRepo     repository.NodeRepository
	reader       readAuthorizer
	logger       *slog.Logger
}

// readAuthorizer answers whether the caller may read nodes in a unit
// (satisfied by the authorization enforcer).
type readAuthorizer interface {
	Can(ctx context.Context, accountID, userID uuid.UUID, action authz.Action, targetOU *uuid.UUID) bool
}

// SetReadAuthorizer wires the per-unit read check. Without it every mark is
// refused (fail-closed).
func (s *Service) SetReadAuthorizer(reader readAuthorizer) { s.reader = reader }

// NewService wires the favorites service. Both repositories are required.
func NewService(favoriteRepo repository.NodeFavoriteRepository, nodeRepo repository.NodeRepository, logger *slog.Logger) *Service {
	if favoriteRepo == nil {
		panic("node_favorite.Service requires a favorite repository")
	}
	if nodeRepo == nil {
		panic("node_favorite.Service requires a node repository")
	}
	return &Service{
		favoriteRepo: favoriteRepo,
		nodeRepo:     nodeRepo,
		logger:       logger.With("service", "node_favorite"),
	}
}

// ListFavoriteNodeIDs returns the ids of the user's favorite nodes within
// the calling account.
func (s *Service) ListFavoriteNodeIDs(ctx context.Context, accountID, userID uuid.UUID) ([]uuid.UUID, error) {
	return s.favoriteRepo.ListNodeIDsByUser(ctx, accountID, userID)
}

// SetFavorite marks or unmarks a node as the user's favorite.
//
// Marking is authorized like every other node route: the node must be in the
// caller's account AND in a unit where the caller may read nodes, and a node
// outside that scope answers exactly like one that does not exist — the write
// must not tell a member of one unit which ids exist in another. Unmarking
// needs neither: it is idempotent, answers alike for every id, and lets a
// member who lost a unit clear the marks they made there.
func (s *Service) SetFavorite(ctx context.Context, accountID, userID, nodeID uuid.UUID, favorite bool) error {
	if favorite {
		n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
		if err != nil {
			return err
		}
		if s.reader == nil || !s.reader.Can(ctx, accountID, userID, authz.ActionNodeRead, n.OUID) {
			return app_errors.ErrNodeNotFound
		}
	}
	return s.favoriteRepo.Set(ctx, userID, nodeID, favorite)
}
