package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// FileService defines the interface for file operations
type FileService interface {
	CreateOrUpdateFromAgent(ctx context.Context, nodeID uuid.UUID, path, content, hash string, version int, status string, errorMsg *string) (*node_file.NodeFile, error)
	GetLatest(ctx context.Context, nodeID uuid.UUID, path string) (*node_file.NodeFile, error)
	GetAllVersions(ctx context.Context, nodeID uuid.UUID, path string) ([]node_file.NodeFile, error)
	GetByVersion(ctx context.Context, nodeID uuid.UUID, path string, version int) (*node_file.NodeFile, error)
	GetByID(ctx context.Context, fileID uuid.UUID) (*node_file.NodeFile, error)
	CreateVersion(ctx context.Context, nodeID uuid.UUID, path, content string) (*node_file.NodeFile, error)

	// DeleteVersions removes the whole version history of one tracked path.
	DeleteVersions(ctx context.Context, nodeID uuid.UUID, path string) error

	// DeleteVersionsByNodeID removes every file version of a node (deletion).
	DeleteVersionsByNodeID(ctx context.Context, nodeID uuid.UUID) error

	// StoredBytes reports how much file content a node currently stores.
	StoredBytes(ctx context.Context, nodeID uuid.UUID) (int64, error)

	// StoredBytesForAccount reports the same across every node of an account.
	// The per-node budget multiplies by the fleet, so a tenant-wide ceiling
	// needs a tenant-wide number.
	StoredBytesForAccount(ctx context.Context, accountID uuid.UUID) (int64, error)
	ApplyFileVersion(ctx context.Context, fileID uuid.UUID) (*node_file.NodeFile, error)
	UpdateStatus(ctx context.Context, nodeID uuid.UUID, path string, version int, status string, errorMsg *string) error
	GetAllPathsByNodeID(ctx context.Context, nodeID uuid.UUID) ([]node_file.NodeFile, error)

	// ListLatestVersionMeta returns the newest version's metadata per path in one
	// query, without reading content. What the Files list needs, and all it
	// needs.
	ListLatestVersionMeta(ctx context.Context, nodeID uuid.UUID) ([]node_file.VersionMeta, error)
}
