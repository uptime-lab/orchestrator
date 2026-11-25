package results

import "github.com/google/uuid"

// NodeFavoriteListResult is the ids of the caller's favorite nodes.
type NodeFavoriteListResult struct {
	NodeIDs []uuid.UUID `json:"node_ids"`
}

// NewNodeFavoriteListResult wraps the favorite node ids (never nil).
func NewNodeFavoriteListResult(ids []uuid.UUID) NodeFavoriteListResult {
	if ids == nil {
		ids = []uuid.UUID{}
	}
	return NodeFavoriteListResult{NodeIDs: ids}
}
