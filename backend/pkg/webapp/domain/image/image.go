package image

import (
	"time"

	"github.com/google/uuid"
)

// FileStorageInfo contains information about the stored file
type FileStorageInfo struct {
	StorageType string    `json:"storage_type"` // "gcp" or "local"
	FilePath    string    `json:"file_path"`    // Path to the file in storage
	FileName    string    `json:"file_name,omitempty"`
	SizeBytes   int64     `json:"size_bytes,omitempty"`
	UploadedAt  time.Time `json:"uploaded_at,omitzero"` // When the current file was uploaded

	// The pre-compression original, kept in storage as a safety copy after a
	// server-side compression. Empty when the current file IS the upload.
	SourceFilePath  string `json:"source_file_path,omitempty"`
	SourceSizeBytes int64  `json:"source_size_bytes,omitempty"`
}

// Compression states for the server-side raw-to-xz compression job.
// An empty status means no compression is running; a successfully compressed
// image is recognizable by its .xz file extension.
const (
	CompressionInProgress = "in_progress"
	CompressionFailed     = "failed"
	// CaptureInProgress marks an image record created for an agent-side disk
	// capture whose upload has not yet arrived. It has no stored file yet, so
	// install_os refuses it ("image file not uploaded") until FinalizeCapture
	// clears the status; it is also excluded from the per-account image cap.
	CaptureInProgress = "capturing"
	// CaptureFailed marks a capture whose upload failed or was abandoned.
	CaptureFailed = "capture_failed"
	// CaptureUploadInProgress marks a capture record an agent is streaming into
	// RIGHT NOW. It is the capture counterpart of UploadInProgress, and it exists
	// because the capture endpoint's capability is a stateless HMAC valid for
	// hours: without an exclusive claim, a replayed PUT raced the legitimate
	// stream through the same check-then-act and both wrote the same object key.
	// The claim is taken atomically from CaptureInProgress/CaptureFailed before
	// the first byte is stored, so exactly one stream per record can be in
	// flight, and a finalized record (empty status) stays unreachable either way.
	//
	// Same column as every other busy state, deliberately (see UploadInProgress).
	// A stale claim left by a dead process is failed back to CaptureFailed at
	// startup, exactly like a stale CaptureInProgress — the agent streams once,
	// so an interrupted capture never resumes.
	CaptureUploadInProgress = "capture_uploading"
	// UploadInProgress marks a record whose file is being replaced right now.
	//
	// It exists so an upload and a compression are mutually exclusive, which
	// checking a status at the top of the upload cannot achieve: a multi-GB
	// upload streams for minutes after that check, and a compress claimed inside
	// that window worked from the record as it was BEFORE the upload — deleting
	// what it believed was the stale object and republishing the previous
	// image's bytes under the same record. The claim is taken
	// before the first byte is stored, so nothing can start in the gap.
	//
	// Deliberately part of the same column as the compression states rather than
	// a second flag: two independent "is this record busy" fields is how a
	// caller ends up checking one of them.
	UploadInProgress = "uploading"
)

// IsBusy reports whether a background job holds this record, so nothing else
// may write its file or its storage info. The single place that answers it —
// the repository predicates and the service both read it, and a second copy is
// how "busy" comes to mean different things in different files.
func IsBusy(compressionStatus string) bool {
	switch compressionStatus {
	case CompressionInProgress, CaptureInProgress, CaptureUploadInProgress, UploadInProgress:
		return true
	}
	return false
}

// BusyStatuses is IsBusy as a list, for SQL predicates.
func BusyStatuses() []string {
	return []string{CompressionInProgress, CaptureInProgress, CaptureUploadInProgress, UploadInProgress}
}

// Image kinds. A plain OS image is uploaded; golden and backup images are
// captured from managed nodes. The kind drives restore behavior: a backup
// carries its source node's identity, so deploying it onto a DIFFERENT node
// requires an identity strip at install time (golden images are already
// generalized at capture).
const (
	KindOS     = "os"
	KindGolden = "golden"
	KindBackup = "backup"
)

// NewCaptureImage creates a pending image record for an agent-side disk
// capture. It carries no file yet; the agent streams the bytes to the
// capability upload endpoint, which finalizes the record. The source node is
// recorded so restores can tell "back onto the same node" from "onto other
// hardware" (the latter needs an identity strip for backup images).
func NewCaptureImage(id, accountID uuid.UUID, title string, generalize bool, sourceNodeID uuid.UUID, sourceNodeName string) *Image {
	now := time.Now()
	kind := KindBackup
	description := "Backup of node " + sourceNodeName
	if generalize {
		kind = KindGolden
		description = "Golden image captured from node " + sourceNodeName
	}
	return &Image{
		ID:                id,
		AccountID:         accountID,
		Title:             title,
		Kind:              kind,
		Description:       description,
		SourceNodeID:      &sourceNodeID,
		SourceNodeName:    sourceNodeName,
		CompressionStatus: CaptureInProgress,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

// Image represents an operating system image in the system
type Image struct {
	ID          uuid.UUID
	AccountID   uuid.UUID
	Title       string
	Version     string
	Description string
	ImageURL    string
	// Kind is os (uploaded), golden (generalized capture) or backup
	// (byte-exact capture of one node). Empty means os (legacy rows).
	Kind string
	// SourceNodeID/SourceNodeName identify the node a golden/backup image was
	// captured from; nil/empty for uploaded images.
	SourceNodeID        *uuid.UUID
	SourceNodeName      string
	FileStorageInfo     *FileStorageInfo `json:"file_storage_info,omitempty"` // Nullable, only set when file is uploaded
	Checksum            string           // SHA256 checksum of the image file
	CompressionStatus   string           // "", CompressionInProgress or CompressionFailed
	CompressionProgress int              // 0-100, meaningful while CompressionInProgress
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// EffectiveKind returns the image kind with the legacy default applied.
func (i *Image) EffectiveKind() string {
	if i.Kind == "" {
		return KindOS
	}
	return i.Kind
}

// NeedsIdentityStripFor reports whether deploying this image onto the given
// node clones another node's machine identity — true for a backup image whose
// source is a different node (or unknown). Golden images are generalized at
// capture and plain OS images never carried an identity.
func (i *Image) NeedsIdentityStripFor(nodeID uuid.UUID) bool {
	if i.EffectiveKind() != KindBackup {
		return false
	}
	return i.SourceNodeID == nil || *i.SourceNodeID != nodeID
}

// NewImage creates a new Image instance with default values
func NewImage(accountID uuid.UUID, title, version, description, imageURL string) *Image {
	now := time.Now()
	return &Image{
		ID:          uuid.New(),
		AccountID:   accountID,
		Title:       title,
		Version:     version,
		Description: description,
		ImageURL:    imageURL,
		Checksum:    "",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// NewImageWithID creates a new Image instance with a specific ID
func NewImageWithID(id, accountID uuid.UUID, title, version, description, imageURL string) *Image {
	now := time.Now()
	return &Image{
		ID:          id,
		AccountID:   accountID,
		Title:       title,
		Version:     version,
		Description: description,
		ImageURL:    imageURL,
		Checksum:    "",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// SetFileStorageInfo updates the file storage information for a fresh upload
// (no retained source file).
func (i *Image) SetFileStorageInfo(storageType, filePath, fileName string, sizeBytes int64) {
	i.FileStorageInfo = &FileStorageInfo{
		StorageType: storageType,
		FilePath:    filePath,
		FileName:    fileName,
		SizeBytes:   sizeBytes,
		UploadedAt:  time.Now(),
	}
	i.UpdatedAt = time.Now()
}

// SetCompressedFileStorageInfo points the image at a server-compressed file
// while recording the retained original (which may live at a distinct path
// for same-extension repacks) as the source. The upload timestamp of the
// original is preserved.
func (i *Image) SetCompressedFileStorageInfo(storageType, filePath, fileName string, sizeBytes int64, sourcePath string, sourceSize int64) {
	uploadedAt := time.Now()
	if i.FileStorageInfo != nil && !i.FileStorageInfo.UploadedAt.IsZero() {
		uploadedAt = i.FileStorageInfo.UploadedAt
	}
	i.FileStorageInfo = &FileStorageInfo{
		StorageType:     storageType,
		FilePath:        filePath,
		FileName:        fileName,
		SizeBytes:       sizeBytes,
		UploadedAt:      uploadedAt,
		SourceFilePath:  sourcePath,
		SourceSizeBytes: sourceSize,
	}
	i.UpdatedAt = time.Now()
}
