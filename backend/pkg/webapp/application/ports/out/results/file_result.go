package results

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// FileResult represents a file in API responses
type FileResult struct {
	ID           string    `json:"id"`
	Path         string    `json:"path"`
	Content      string    `json:"content,omitempty"`
	Version      int       `json:"version"`
	Status       string    `json:"status"`
	Hash         string    `json:"hash"`
	ErrorMessage string    `json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// NewFileResult creates a new FileResult from a domain NodeFile
func NewFileResult(domainFile *node_file.NodeFile) FileResult {
	errorMsg := ""
	if domainFile.ErrorMessage != nil {
		errorMsg = *domainFile.ErrorMessage
	}

	return FileResult{
		ID:           domainFile.ID.String(),
		Path:         domainFile.Path,
		Content:      domainFile.Content,
		Version:      domainFile.Version,
		Status:       domainFile.Status,
		Hash:         domainFile.Hash,
		ErrorMessage: errorMsg,
		CreatedAt:    domainFile.CreatedAt,
		UpdatedAt:    domainFile.UpdatedAt,
	}
}

// FileListResult represents a list of files in API responses
type FileListResult struct {
	Files []FileResult `json:"files"`
}

// NewFileListResult creates a new FileListResult from a slice of domain files.
//
// The list carries NO file content, deliberately. Content is a privileged read
// (a tracked file can be a netplan config with a PSK or an application .env),
// so it is served only by the single-file endpoint under its own action. The
// list is read at node-read level, which the Viewer role holds — inlining
// content here would hand every tracked file on every node to a role that is
// explicitly denied secret values, node credentials and the terminal.
func NewFileListResult(files []node_file.NodeFile) *FileListResult {
	fileResults := make([]FileResult, len(files))
	for i, file := range files {
		fileResults[i] = NewFileResult(&file)
		fileResults[i].Content = ""
	}

	return &FileListResult{
		Files: fileResults,
	}
}

// FileVersionsResult represents a list of file versions in API responses
type FileVersionsResult struct {
	Versions []FileResult `json:"versions"`
}

// NewFileVersionsResult creates a new FileVersionsResult from a slice of domain
// files. Like the list, it is metadata only — the version history of a file is
// as sensitive as its current content, and the caller fetches a single version
// through the content endpoint.
func NewFileVersionsResult(files []node_file.NodeFile) *FileVersionsResult {
	fileResults := make([]FileResult, len(files))
	for i, file := range files {
		fileResults[i] = NewFileResult(&file)
		fileResults[i].Content = ""
	}

	return &FileVersionsResult{
		Versions: fileResults,
	}
}

// FileUpdateResult represents the result of a file update operation
type FileUpdateResult struct {
	// ID is the version's id: what POST /nodes/{id}/files/{file_id}/apply
	// takes, so a saved version can be applied without listing the versions
	// first.
	ID      string `json:"id"`
	Path    string `json:"path"`
	Version int    `json:"version"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

// NewFileUpdateResult creates a new FileUpdateResult
func NewFileUpdateResult(id uuid.UUID, path string, version int, status, message string) *FileUpdateResult {
	return &FileUpdateResult{
		ID:      id.String(),
		Path:    path,
		Version: version,
		Status:  status,
		Message: message,
	}
}

// TrackedFileResult is one row of the node's Files list: the tracked record
// plus the latest version's metadata. Never any content — see NewFileListResult.
type TrackedFileResult struct {
	Path          string     `json:"path"`
	Source        string     `json:"source"`
	Protected     bool       `json:"protected"`
	NeverRead     bool       `json:"never_read"`
	CanApply      bool       `json:"can_apply"`
	Mode          string     `json:"mode,omitempty"`
	Owner         string     `json:"owner,omitempty"`
	Group         string     `json:"group,omitempty"`
	Size          int64      `json:"size"`
	LastReadAt    int64      `json:"last_read_at,omitempty"`
	LastAppliedAt int64      `json:"last_applied_at,omitempty"`
	Version       int        `json:"version,omitempty"`
	Status        string     `json:"status,omitempty"`
	Hash          string     `json:"hash,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
}

// TrackedFileListResult is the Files list response.
type TrackedFileListResult struct {
	Files []TrackedFileResult `json:"files"`
}

// NewTrackedFileResult builds one row. latest is the newest version's metadata
// and may be nil when the path has no version history at all — deliberately
// metadata, not a NodeFile: the list must never carry, or even decrypt, content.
func NewTrackedFileResult(tracked node_file.TrackedFile, latest *node_file.VersionMeta) TrackedFileResult {
	row := TrackedFileResult{
		Path:      tracked.Path,
		Source:    tracked.Source,
		Protected: tracked.IsProtected(),
		NeverRead: tracked.NeverRead(),
		CanApply:  tracked.CanApply(),
		Mode:      tracked.Mode,
		Owner:     tracked.Owner,
		Group:     tracked.Group,
		Size:      tracked.Size,
	}
	if tracked.LastReadAt != nil {
		row.LastReadAt = tracked.LastReadAt.Unix()
	}
	if tracked.LastAppliedAt != nil {
		row.LastAppliedAt = tracked.LastAppliedAt.Unix()
	}
	if latest != nil {
		row.Version = latest.Version
		row.Status = latest.Status
		row.Hash = latest.Hash
		row.UpdatedAt = &latest.UpdatedAt
	}
	return row
}
