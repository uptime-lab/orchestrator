package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// NodeTrackedFileRepository stores which files the platform follows on a node.
type NodeTrackedFileRepository interface {
	// Create adds a tracked file.
	Create(ctx context.Context, file *node_file.TrackedFile) error

	// CreateUnderLimit creates a tracked file only while the node is still
	// under its file cap, returning the existing row when the path is already
	// tracked. The cap check and the insert are one atomic step: read-then-
	// create let concurrent track polls for different paths each see the node
	// below the limit and all insert. limit <= 0 means unlimited.
	CreateUnderLimit(ctx context.Context, file *node_file.TrackedFile, limit int) (*node_file.TrackedFile, error)

	// Update saves metadata changes (mode/owner/size/timestamps).
	Update(ctx context.Context, file *node_file.TrackedFile) error

	// GetByNodeIDAndPath returns one tracked file, or ErrNotTracked.
	GetByNodeIDAndPath(ctx context.Context, nodeID uuid.UUID, path string) (*node_file.TrackedFile, error)

	// ListByNodeID returns every tracked file for a node, path-ordered.
	ListByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_file.TrackedFile, error)

	// CountByNodeID returns how many files a node tracks (for the cap).
	CountByNodeID(ctx context.Context, nodeID uuid.UUID) (int64, error)

	// Delete removes a tracked file by id.
	Delete(ctx context.Context, id uuid.UUID) error

	// DeleteByNodeID removes every tracked file of a node (node deletion).
	DeleteByNodeID(ctx context.Context, nodeID uuid.UUID) error
}
