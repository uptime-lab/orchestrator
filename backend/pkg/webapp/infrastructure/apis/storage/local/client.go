package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/agentpkg"
)

// LocalStorageClient implements the storage.StorageClient interface for local filesystem storage
type LocalStorageClient struct {
	baseDir string
	baseURL string
}

// NewLocalStorageClient creates a new local storage client
func NewLocalStorageClient(baseDir, baseURL string) (*LocalStorageClient, error) {
	// Sanitize and normalize base directory path
	baseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base directory path: %w", err)
	}

	// Create base directory if it doesn't exist
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	// Verify base directory is writable
	testFile := filepath.Join(baseDir, ".test")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		return nil, fmt.Errorf("base directory is not writable: %w", err)
	}
	os.Remove(testFile)

	return &LocalStorageClient{
		baseDir: baseDir,
		baseURL: baseURL,
	}, nil
}

// sanitizePath ensures the file path is safe and within the base directory
func (l *LocalStorageClient) sanitizePath(name string) (string, error) {
	// Remove leading slashes and normalize
	name = strings.TrimPrefix(name, "/")
	name = strings.TrimPrefix(name, "\\")

	// Clean the path to remove ".." and other dangerous patterns
	cleanPath := filepath.Clean(name)

	// Prevent directory traversal
	if strings.Contains(cleanPath, "..") {
		return "", fmt.Errorf("invalid path: contains directory traversal")
	}

	// Join with base directory and get absolute path
	fullPath := filepath.Join(l.baseDir, cleanPath)
	fullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}

	// Ensure the resolved path is within base directory
	baseDirAbs, err := filepath.Abs(l.baseDir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve base directory: %w", err)
	}

	// filepath.Rel, not strings.HasPrefix: a prefix test accepts a SIBLING
	// whose name merely starts with the base ("/data/images-evil" passes a
	// prefix check against "/data/images"). Not reachable today — the ".."
	// check above and UUID-constructed paths block it — but containment should
	// not depend on callers staying well-behaved.
	rel, err := filepath.Rel(baseDirAbs, fullPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid path: outside base directory")
	}

	return fullPath, nil
}

// getMetadataPath returns the path for the metadata file
func (l *LocalStorageClient) getMetadataPath(filePath string) string {
	return filePath + ".meta"
}

// StoreWithOptions stores a blob file with advanced options
func (l *LocalStorageClient) StoreWithOptions(ctx context.Context, name string, content io.Reader, options *storage.StorageOptions) (string, error) {
	filePath, err := l.sanitizePath(name)
	if err != nil {
		return "", err
	}

	// Create parent directories if they don't exist
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return "", fmt.Errorf("failed to create parent directories: %w", err)
	}

	// Write to a UNIQUE temp file and rename into place so a failed or
	// interrupted upload never truncates an existing file at the same path.
	// The temp name must be unique per call: two concurrent stores to the same
	// path (an agent retry mid-stream, or a replayed capability upload) that
	// shared one ".tmp" would interleave their bytes and rename a corrupt file.
	tmpFile, err := os.CreateTemp(filepath.Dir(filePath), filepath.Base(filePath)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	tmpPath := tmpFile.Name()
	file := tmpFile

	if _, err := io.Copy(file, content); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write file content: %w", err)
	}

	if err := file.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to flush file content: %w", err)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to finalize file: %w", err)
	}

	// Store metadata if provided
	if options != nil && options.Metadata != nil {
		metadataPath := l.getMetadataPath(filePath)
		metadataJSON, err := json.Marshal(options.Metadata)
		if err != nil {
			return "", fmt.Errorf("failed to marshal metadata: %w", err)
		}
		if err := os.WriteFile(metadataPath, metadataJSON, 0644); err != nil {
			return "", fmt.Errorf("failed to write metadata: %w", err)
		}
	}

	// Return the file path relative to base directory
	relPath, err := filepath.Rel(l.baseDir, filePath)
	if err != nil {
		relPath = name // Fallback to original name
	}
	return relPath, nil
}

// Retrieve retrieves a blob file by name
func (l *LocalStorageClient) Retrieve(ctx context.Context, name string) (io.ReadCloser, error) {
	filePath, err := l.sanitizePath(name)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", name)
		}
		return nil, fmt.Errorf("failed to open file: %w", err)
	}

	return file, nil
}

// GetSignedURL generates a signed URL for the given file name
func (l *LocalStorageClient) GetSignedURL(ctx context.Context, name string, expiration time.Duration) (string, error) {
	// Check if file exists
	exists, err := l.Exists(ctx, name)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("file does not exist: %s", name)
	}

	// Generate URL using baseURL
	if l.baseURL != "" {
		// Remove leading slash from name if present
		cleanName := strings.TrimPrefix(name, "/")
		return fmt.Sprintf("%s/%s", strings.TrimSuffix(l.baseURL, "/"), cleanName), nil
	}

	// Fallback to file path if no baseURL configured
	filePath, err := l.sanitizePath(name)
	if err != nil {
		return "", err
	}
	return filePath, nil
}

// Delete removes a blob file by name
func (l *LocalStorageClient) Delete(ctx context.Context, name string) error {
	filePath, err := l.sanitizePath(name)
	if err != nil {
		return err
	}

	// Delete the file
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist, consider it deleted
		}
		return fmt.Errorf("failed to delete file: %w", err)
	}

	// Delete metadata file if it exists
	metadataPath := l.getMetadataPath(filePath)
	os.Remove(metadataPath) // Ignore error if metadata doesn't exist

	return nil
}

// Exists checks if a blob file exists by name
func (l *LocalStorageClient) Exists(ctx context.Context, name string) (bool, error) {
	filePath, err := l.sanitizePath(name)
	if err != nil {
		return false, err
	}

	_, err = os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check file existence: %w", err)
	}

	return true, nil
}

// GetFileInfo returns information about a stored file
func (l *LocalStorageClient) GetFileInfo(ctx context.Context, name string) (*file.FileInfo, error) {
	filePath, err := l.sanitizePath(name)
	if err != nil {
		return nil, err
	}

	stat, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get file info: %w", err)
	}

	fileInfo := &file.FileInfo{
		Name:         filepath.Base(filePath),
		FilePath:     name,
		Size:         stat.Size(),
		LastModified: stat.ModTime(),
	}

	// Try to get content type from metadata or infer from extension
	metadataPath := l.getMetadataPath(filePath)
	if metadataData, err := os.ReadFile(metadataPath); err == nil {
		var metadata map[string]string
		if err := json.Unmarshal(metadataData, &metadata); err == nil {
			fileInfo.Metadata = metadata
			if contentType, ok := metadata["content_type"]; ok {
				fileInfo.ContentType = contentType
			}
		}
	}

	// If no content type in metadata, try to infer from extension
	if fileInfo.ContentType == "" {
		ext := filepath.Ext(filePath)
		fileInfo.ContentType = getContentTypeFromExtension(ext)
	}

	return fileInfo, nil
}

// getContentTypeFromExtension returns a content type based on file extension
func getContentTypeFromExtension(ext string) string {
	contentTypes := map[string]string{
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".png":  "image/png",
		".gif":  "image/gif",
		".pdf":  "application/pdf",
		".txt":  "text/plain",
		".html": "text/html",
		".css":  "text/css",
		".js":   "application/javascript",
		".json": "application/json",
		".xml":  "application/xml",
		".zip":  "application/zip",
		".mp4":  "video/mp4",
		".mp3":  "audio/mpeg",
	}

	if contentType, ok := contentTypes[strings.ToLower(ext)]; ok {
		return contentType
	}
	return "application/octet-stream"
}

// GetStorageType returns the storage type identifier
func (l *LocalStorageClient) GetStorageType() string {
	return "local"
}

// ListAgentVersions returns the distinct agent versions available under basePath,
// parsed from the agent package filenames. The parsing and ordering live in
// agentpkg so the S3 backend answers the same endpoint identically.
func (l *LocalStorageClient) ListAgentVersions(basePath string) ([]string, error) {
	names, err := l.agentFileNames(basePath)
	if err != nil {
		return nil, err
	}
	return agentpkg.Versions(names), nil
}

// ListAgentVersionsByArch is ListAgentVersions per architecture.
func (l *LocalStorageClient) ListAgentVersionsByArch(basePath string) (map[string][]string, error) {
	names, err := l.agentFileNames(basePath)
	if err != nil {
		return nil, err
	}
	return agentpkg.VersionsByArch(names), nil
}

func (l *LocalStorageClient) agentFileNames(basePath string) ([]string, error) {
	dir := filepath.Join(l.baseDir, strings.Trim(basePath, "/"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	return names, nil
}
