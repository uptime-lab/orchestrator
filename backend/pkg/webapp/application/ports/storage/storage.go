package storage

import (
	"context"
	"io"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/file"
)

// StorageClient defines the interface for blob storage operations
// Compatible with Google Cloud Storage and AWS S3
type StorageClient interface {

	// StoreWithOptions stores a blob file with advanced options
	StoreWithOptions(ctx context.Context, name string, content io.Reader, options *StorageOptions) (string, error)

	// Retrieve retrieves a blob file by name
	// Returns the file content as a reader
	Retrieve(ctx context.Context, name string) (io.ReadCloser, error)

	// GetSignedURL generates a signed URL for the given file name
	// The signed URL allows temporary access to the file without authentication
	// expiration specifies how long the signed URL should be valid
	GetSignedURL(ctx context.Context, name string, expiration time.Duration) (string, error)

	// Delete removes a blob file by name
	Delete(ctx context.Context, name string) error

	// Exists checks if a blob file exists by name
	// Returns true if the file exists, false otherwise
	Exists(ctx context.Context, name string) (bool, error)

	// GetFileInfo returns information about a stored file
	// Returns nil if the file doesn't exist
	GetFileInfo(ctx context.Context, name string) (*file.FileInfo, error)

	// NOTE: no Set/GetFileMetadata. Both were on this port, implemented three
	// times (local client, test fake, and a sidecar `.meta` file format) and
	// called from nowhere — a contract every future storage backend would have
	// had to satisfy to serve no caller. Image metadata lives in Postgres, which
	// is where it can be queried and transacted.

	// GetStorageType returns the storage type identifier (e.g., "gcp", "local")
	GetStorageType() string
}

// StorageOptions contains configuration options for storage operations
type StorageOptions struct {
	// ContentType specifies the MIME type of the file
	ContentType string `json:"content_type,omitempty"`

	// Metadata contains custom key-value pairs to store with the file
	Metadata map[string]string `json:"metadata,omitempty"`

	// CacheControl specifies caching behavior (e.g., "public, max-age=3600")
	CacheControl string `json:"cache_control,omitempty"`

	// ContentDisposition specifies how the file should be handled by browsers
	ContentDisposition string `json:"content_disposition,omitempty"`

	// ContentEncoding specifies the encoding of the file content
	ContentEncoding string `json:"content_encoding,omitempty"`

	// ACL specifies access control list (implementation specific)
	ACL string `json:"acl,omitempty"`

	// StorageClass specifies the storage class (e.g., "STANDARD", "STANDARD_IA" for S3)
	StorageClass string `json:"storage_class,omitempty"`
}

// Storage type identifiers returned by GetStorageType.
const (
	TypeLocal = "local"
	TypeGCP   = "gcp"
	TypeS3    = "s3"
)

// ServedThroughCDN reports whether the platform's own /cdn/* routes proxy bytes
// out of this backend, rather than handing the client a URL that points straight
// at the storage provider.
//
// Every caller asks THIS question, never "is the storage local": asked that
// way, adding a storage backend unregisters the whole CDN route family —
// including /cdn/agent/..., which is how install.sh and every update_agent
// fetch the agent package. That is a fleet-wide outage produced by a config
// change, and the 404 it returns looks like a missing file rather than a
// missing route.
//
// S3 proxies rather than redirects on purpose. It avoids metered bucket egress
// where bucket-to-server traffic is cheaper than bucket-to-client, and it keeps the cdnsign capability model and the URL shape identical for every
// client — the agent, install.sh and the browser — so no client behaviour has to
// be re-verified when the storage backend changes.
func ServedThroughCDN(storageType string) bool {
	switch storageType {
	case TypeLocal, TypeS3:
		return true
	default:
		// GCP hands out provider signed URLs and is left as it was: it is not
		// exercised by the test suite, so it is not changed untested.
		return false
	}
}
