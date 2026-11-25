package results

import (
	"time"
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
)

// ImageResult represents an image in API responses
type ImageResult struct {
	ID                  uuid.UUID              `json:"id"`
	AccountID           uuid.UUID              `json:"account_id"`
	Title               string                 `json:"title"`
	Version             string                 `json:"version"`
	Description         string                 `json:"description"`
	ImageURL            string                 `json:"image_url"`
	Kind                string                 `json:"kind"`
	SourceNodeID        string                 `json:"source_node_id,omitempty"`
	SourceNodeName      string                 `json:"source_node_name,omitempty"`
	FileStorageInfo     *image.FileStorageInfo `json:"file_storage_info,omitempty"`
	Checksum            string                 `json:"checksum,omitempty"`
	CompressionStatus   string                 `json:"compression_status,omitempty"`
	CompressionProgress int                    `json:"compression_progress,omitempty"`
	CreatedAt           time.Time                  `json:"created_at"`
	UpdatedAt           time.Time                  `json:"updated_at"`
}

// NewImageResult creates a new ImageResult from a domain Image
func NewImageResult(domainImage *image.Image) ImageResult {
	fileStorageInfo := domainImage.FileStorageInfo
	// Files stored before upload timestamps existed have a zero UploadedAt;
	// fall back to the image creation time instead of surfacing year 1.
	if fileStorageInfo != nil && fileStorageInfo.UploadedAt.IsZero() {
		infoCopy := *fileStorageInfo
		infoCopy.UploadedAt = domainImage.CreatedAt
		fileStorageInfo = &infoCopy
	}
	sourceNodeID := ""
	if domainImage.SourceNodeID != nil {
		sourceNodeID = domainImage.SourceNodeID.String()
	}
	return ImageResult{
		ID:                  domainImage.ID,
		AccountID:           domainImage.AccountID,
		Title:               domainImage.Title,
		Version:             domainImage.Version,
		Description:         domainImage.Description,
		ImageURL:            domainImage.ImageURL,
		Kind:                domainImage.EffectiveKind(),
		SourceNodeID:        sourceNodeID,
		SourceNodeName:      domainImage.SourceNodeName,
		FileStorageInfo:     fileStorageInfo,
		Checksum:            domainImage.Checksum,
		CompressionStatus:   domainImage.CompressionStatus,
		CompressionProgress: domainImage.CompressionProgress,
		CreatedAt:           domainImage.CreatedAt,
		UpdatedAt:           domainImage.UpdatedAt,
	}
}

// ImageListResult is a list of images in API responses.
//
// It carries a COUNT and no limit/offset. Those two were here, set to
// (len(items), 0) at the only call site — a client reading them as a page size
// and an offset would page forever over the same whole list, believing it was
// paginating. An unpaginated list saying so is the honest shape; when this list
// does gain paging it takes the `page`/`per_page`/`total` envelope the audit,
// execution and secret lists already use, not a second convention.
type ImageListResult struct {
	Images []ImageResult `json:"images"`
	Count  int           `json:"count"`
}

// NewImageListResult builds the list envelope.
func NewImageListResult(images []ImageResult) *ImageListResult {
	return &ImageListResult{
		Images: images,
		Count:  len(images),
	}
}
