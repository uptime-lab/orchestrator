package image_upload

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/google/uuid"
)

// allowedExtensions lists the file extensions accepted for OS image uploads.
// Raw images (.img/.iso) are written to devices as-is; .xz and .zst are
// decompressed by the agent while streaming during install_os. Everything
// else is rejected so the catalog only holds formats install_os can deploy.
var allowedExtensions = map[string]struct{}{
	".iso": {},
	".img": {},
	".xz":  {},
	".zst": {},
}

// ImageUploadCommand represents a command to upload a file for an image
type ImageUploadCommand struct {
	ID          uuid.UUID            `json:"-"` // Set from path parameter, not from request
	FileName    string               `json:"-"` // Original file name from the multipart form
	ContentType string               `json:"-"` // Content type reported by the client
	SizeBytes   int64                `json:"-"` // Declared file size from the multipart form
	AuthContext commands.AuthContext `json:"-"` // Set internally, not from request
}

// Validate checks that the uploaded file looks like an OS image.
func (c *ImageUploadCommand) Validate() error {
	if c.ID == uuid.Nil {
		return fmt.Errorf("image ID is required")
	}
	if strings.TrimSpace(c.FileName) == "" {
		return fmt.Errorf("file name is required")
	}
	if c.SizeBytes <= 0 {
		return fmt.Errorf("file is empty")
	}
	ext := strings.ToLower(filepath.Ext(c.FileName))
	if _, ok := allowedExtensions[ext]; !ok {
		return fmt.Errorf("unsupported file type %q: allowed types are %s", ext, allowedExtensionsList())
	}
	return nil
}

// allowedExtensionsList returns the allowlist as a stable, human-readable string.
func allowedExtensionsList() string {
	exts := make([]string, 0, len(allowedExtensions))
	for ext := range allowedExtensions {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return strings.Join(exts, ", ")
}
