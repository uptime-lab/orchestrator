package repository

import (
	"context"

	"github.com/google/uuid"
)

// NodeFavoriteRepository persists per-user node favorites. Favorites are a
// personal view: they carry no policy semantics and do not affect OU
// membership.
type NodeFavoriteRepository interface {
	// ListNodeIDsByUser returns the ids of the nodes the user marked favorite
	// WITHIN the given account. The marks table is keyed (user, node) alone, so
	// without the account filter a multi-account user's list leaks node UUIDs
	// from their other accounts into whichever one they are asking from.
	ListNodeIDsByUser(ctx context.Context, accountID, userID uuid.UUID) ([]uuid.UUID, error)
	// Set marks or unmarks a node as the user's favorite (idempotent).
	Set(ctx context.Context, userID, nodeID uuid.UUID, favorite bool) error
	// DeleteByNode removes the favorite marks of a deleted node for all users.
	DeleteByNode(ctx context.Context, nodeID uuid.UUID) error
}
