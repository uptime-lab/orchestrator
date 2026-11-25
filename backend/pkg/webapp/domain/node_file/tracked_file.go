package node_file

import (
	"time"

	"github.com/google/uuid"
)

// Source records who put a file on the list.
const (
	// SourcePlatform — a file the platform puts on the node itself. The agent
	// reports these unasked; they cannot be untracked.
	SourcePlatform = "platform"
	// SourceUser — a file someone asked to track.
	SourceUser = "user"
)

// TrackedFile is the identity of a file the platform follows on a node: one row
// per (node, path), independent of its version history.
//
// It is a separate record from NodeFile, which is the version log, because a
// track has to survive states the log cannot express — a track whose first read
// failed has no versions at all, and version retention deletes old rows without
// meaning "stop following this file". Mode, owner and group belong to the file,
// not to a version: keeping them per version would mean every stale row carries
// a stale mode, and Apply reads exactly the wrong one.
type TrackedFile struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	NodeID    uuid.UUID
	Path      string
	Source    string

	// Mode/Owner/Group are what the device reported at the last successful read.
	// Apply sends them back so writing a file never changes its permissions —
	// an atomic write leaves a fresh inode owned by root otherwise.
	Mode  string
	Owner string
	Group string
	Size  int64

	LastReadAt    *time.Time
	LastAppliedAt *time.Time
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewTrackedFile creates a tracked-file record.
func NewTrackedFile(accountID, nodeID uuid.UUID, path, source string, createdBy uuid.UUID) *TrackedFile {
	now := time.Now()
	return &TrackedFile{
		ID:        uuid.New(),
		AccountID: accountID,
		NodeID:    nodeID,
		Path:      path,
		Source:    source,
		CreatedBy: createdBy,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// IsProtected reports whether this file may not be untracked or deleted.
func (t *TrackedFile) IsProtected() bool {
	return t.Source == SourcePlatform || IsProtected(t.Path)
}

// NeverRead reports whether the platform has never successfully read the file
// from the device. Such a track is a placeholder — it is listed so the user can
// see it and remove it, but it has no content to edit or apply.
func (t *TrackedFile) NeverRead() bool {
	return t.LastReadAt == nil
}

// CanApply reports whether Apply is safe. Without a recorded mode and owner the
// agent would write the file back with default permissions, which silently
// widens a 0600 file to 0644 root:root. Refresh the file first.
func (t *TrackedFile) CanApply() bool {
	if IsPlatformPath(t.Path) {
		// The platform config files predate tracking metadata and have always
		// been written with the agent's defaults; requiring a refresh here would
		// break config.txt editing for every existing node.
		return true
	}
	return t.Mode != "" && t.Owner != ""
}
