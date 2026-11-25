package node_file

import (
	"time"

	"github.com/google/uuid"
)

// NodeFile represents a file version for a node
type NodeFile struct {
	ID           uuid.UUID
	NodeID       uuid.UUID
	Path         string  // File path (e.g., "/boot/config.txt")
	Content      string  // File content
	Hash         string  // SHA256 hash of content
	Version      int     // Version number (auto-increment per node+path)
	Status       string  // "applying", "applied", "failed"
	ErrorMessage *string // Error message if status is "failed" (nullable)
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Status constants for file status
const (
	StatusSaved    = "saved"    // File version saved but not yet applied
	StatusApplying = "applying" // File version is being applied to agent
	StatusApplied  = "applied"  // File version successfully applied
	StatusFailed   = "failed"   // File version application failed
)

// NewNodeFile creates a new NodeFile instance
func NewNodeFile(nodeID uuid.UUID, path, content, hash string, version int, status string) *NodeFile {
	now := time.Now()
	return &NodeFile{
		ID:        uuid.New(),
		NodeID:    nodeID,
		Path:      path,
		Content:   content,
		Hash:      hash,
		Version:   version,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// VersionMeta is the newest version's metadata for one path, without its
// content. The Files list needs exactly these fields, and reading them without
// the content is what keeps the list one query instead of one per row — and
// what keeps a row whose content cannot be decrypted from being rendered as a
// file that was never read.
type VersionMeta struct {
	Path      string
	Version   int
	Status    string
	Hash      string
	UpdatedAt time.Time
}
