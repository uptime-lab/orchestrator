package results

import (
	"github.com/google/uuid"
)

// ImageDownloadResult is the capability handed to a caller who wants the image
// bytes back out of the platform.
//
// It is a URL rather than the bytes themselves because these are multi-gigabyte
// objects and a browser cannot attach an Authorization header to a navigation:
// fetching through the JSON API would mean buffering the whole image in the tab.
// The URL is the same time-bounded /cdn capability install_os already uses, so
// there is one signing scheme, one route family and one thing to reason about.
//
// The consequence to know: once minted, this URL grants the bytes to whoever
// holds it until it expires, with no further authentication. That is why the
// mint is short-lived, gated on image *manage* rather than read, and recorded in
// the audit trail.
type ImageDownloadResult struct {
	ID  uuid.UUID `json:"id"`
	URL string    `json:"url"`
	// FileName is the name the download should land under — the name the file
	// was uploaded with, or a generated one for a captured image. Storage keys
	// are `<account>/<image-id>.<ext>`, so without this a customer offboarding
	// three images gets three UUIDs.
	FileName  string `json:"file_name"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	// Checksum lets the recipient verify the copy they took. It is the same
	// SHA-256 the upload computed while streaming.
	Checksum  string `json:"checksum,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}
