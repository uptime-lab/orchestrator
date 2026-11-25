package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// NodeFileRepository defines the interface for node file-related database operations
type NodeFileRepository interface {
	// CreateNextVersion inserts a file version, allocating its number in the same
	// statement, and writes the allocated number back into file.Version.
	//
	// It is deliberately the ONLY way to insert a version. Allocation used to be a
	// GetMaxVersion read followed by an unrelated Create, so two concurrent saves
	// of the same path — the operator applying an edit while the agent reports the
	// file, which is one keystroke apart in practice — both read the same maximum
	// and wrote the same number. There is no second entry point to reintroduce
	// that with.
	CreateNextVersion(ctx context.Context, file *node_file.NodeFile) error

	// Update updates an existing file version
	Update(ctx context.Context, file *node_file.NodeFile) error

	// GetByNodeIDAndPath retrieves all versions of a file for a node and path
	GetByNodeIDAndPath(ctx context.Context, nodeID uuid.UUID, path string) ([]node_file.NodeFile, error)

	// GetLatestByNodeIDAndPath retrieves the latest version of a file for a node and path
	GetLatestByNodeIDAndPath(ctx context.Context, nodeID uuid.UUID, path string) (*node_file.NodeFile, error)

	// GetByNodeIDPathAndVersion retrieves a specific version of a file
	GetByNodeIDPathAndVersion(ctx context.Context, nodeID uuid.UUID, path string, version int) (*node_file.NodeFile, error)

	// ListLatestVersionMeta returns the newest version's metadata for every path
	// a node has history for, in ONE query and without reading content. The Files
	// list uses this instead of a GetLatest per row, which was a query and a full
	// decryption per file for four scalar fields.
	ListLatestVersionMeta(ctx context.Context, nodeID uuid.UUID) ([]node_file.VersionMeta, error)

	// GetAllPathsByNodeID retrieves all unique paths for a node (returns latest version of each)
	GetAllPathsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_file.NodeFile, error)

	// GetByID retrieves a file version by its ID
	GetByID(ctx context.Context, fileID uuid.UUID) (*node_file.NodeFile, error)

	// DeleteByNodeIDAndPath removes every version of one path (untrack).
	DeleteByNodeIDAndPath(ctx context.Context, nodeID uuid.UUID, path string) error

	// DeleteByNodeID removes every file version of a node (node deletion).
	DeleteByNodeID(ctx context.Context, nodeID uuid.UUID) error

	// PruneVersions keeps only the newest `keep` versions of one path.
	PruneVersions(ctx context.Context, nodeID uuid.UUID, path string, keep int) error

	// SumContentBytesByNodeID returns how many bytes of file content a node
	// currently stores, as stored (encrypted), for the per-node budget.
	SumContentBytesByNodeID(ctx context.Context, nodeID uuid.UUID) (int64, error)

	// SumContentBytesByAccountID returns the same across every node of an
	// account, for the tenant-wide ceiling. Reads the denormalized account_id.
	SumContentBytesByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error)
}
