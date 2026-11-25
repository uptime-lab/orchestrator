package file

import (
	"time"
)

// FileInfo contains metadata about a stored file
type FileInfo struct {
	Name         string            `json:"name"`
	FilePath     string            `json:"file_path"`
	Size         int64             `json:"size"`
	ContentType  string            `json:"content_type"`
	LastModified time.Time         `json:"last_modified"`
	ETag         string            `json:"etag,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// FileNotFoundError represents an error when a file is not found
type FileNotFoundError struct {
	FilePath string
}

func (e *FileNotFoundError) Error() string {
	return "file not found: " + e.FilePath
}
