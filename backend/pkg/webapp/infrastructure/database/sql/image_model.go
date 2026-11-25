package sql

import (
	"encoding/json"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/image"
	"github.com/google/uuid"
)

// ImageModel is the SQL representation of an image
type ImageModel struct {
	ID                  uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID           uuid.UUID  `gorm:"column:account_id;type:uuid;index;not null"`
	Title               string     `gorm:"column:title;size:255;not null"`
	Version             string     `gorm:"column:version;size:100;not null"`
	Description         string     `gorm:"column:description;type:text"`
	ImageURL            string     `gorm:"column:image_url;size:500;not null"`
	Kind                string     `gorm:"column:kind;size:20;not null;default:os"`
	SourceNodeID        *uuid.UUID `gorm:"column:source_node_id;type:uuid"`
	SourceNodeName      string     `gorm:"column:source_node_name;size:255"`
	FileStorageInfo     *string    `gorm:"column:file_storage_info;type:jsonb"` // Nullable JSON storage for FileStorageInfo
	Checksum            string     `gorm:"column:checksum;size:64"`             // SHA256 checksum
	CompressionStatus   string     `gorm:"column:compression_status;size:20"`   // "", "in_progress" or "failed"
	CompressionProgress int        `gorm:"column:compression_progress"`         // 0-100 while in_progress
	CreatedAt           time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (ImageModel) TableName() string {
	return "images"
}

// ToDomain converts the SQL model to a domain model
func (m *ImageModel) ToDomain() *image.Image {
	domainImage := &image.Image{
		ID:                  m.ID,
		AccountID:           m.AccountID,
		Title:               m.Title,
		Version:             m.Version,
		Description:         m.Description,
		ImageURL:            m.ImageURL,
		Kind:                m.Kind,
		SourceNodeID:        m.SourceNodeID,
		SourceNodeName:      m.SourceNodeName,
		Checksum:            m.Checksum,
		CompressionStatus:   m.CompressionStatus,
		CompressionProgress: m.CompressionProgress,
		CreatedAt:           m.CreatedAt,
		UpdatedAt:           m.UpdatedAt,
	}

	// Parse FileStorageInfo from JSON
	if m.FileStorageInfo != nil && *m.FileStorageInfo != "" {
		var fileStorageInfo image.FileStorageInfo
		if err := json.Unmarshal([]byte(*m.FileStorageInfo), &fileStorageInfo); err == nil {
			domainImage.FileStorageInfo = &fileStorageInfo
		}
	}

	return domainImage
}

// FromDomain updates the SQL model from a domain model
func (m *ImageModel) FromDomain(img *image.Image) {
	m.ID = img.ID
	m.AccountID = img.AccountID
	m.Title = img.Title
	m.Version = img.Version
	m.Description = img.Description
	m.ImageURL = img.ImageURL
	m.Kind = img.Kind
	m.SourceNodeID = img.SourceNodeID
	m.SourceNodeName = img.SourceNodeName
	m.Checksum = img.Checksum
	m.CompressionStatus = img.CompressionStatus
	m.CompressionProgress = img.CompressionProgress
	m.CreatedAt = img.CreatedAt
	m.UpdatedAt = img.UpdatedAt

	// Serialize FileStorageInfo to JSON - set to nil if FileStorageInfo is nil
	if img.FileStorageInfo != nil {
		if jsonData, err := json.Marshal(img.FileStorageInfo); err == nil {
			jsonStr := string(jsonData)
			m.FileStorageInfo = &jsonStr
		} else {
			m.FileStorageInfo = nil
		}
	} else {
		m.FileStorageInfo = nil
	}
}
