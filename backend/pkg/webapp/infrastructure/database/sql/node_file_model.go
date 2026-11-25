package sql

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// NodeFileModel is the SQL representation of a node file
type NodeFileModel struct {
	ID uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	// AccountID is denormalized from the node. It is a second tenancy layer —
	// every read is scoped through the node already — and the enabler for a
	// per-account storage ceiling, which cannot be asked of a per-node table
	// without joining every row back to nodes. Nullable so the AutoMigrate that
	// adds the column does not fail on existing rows; Setup backfills them.
	AccountID *uuid.UUID `gorm:"column:account_id;type:uuid;index"`
	NodeID    uuid.UUID  `gorm:"column:node_id;type:uuid;index;not null"`
	Path      string     `gorm:"column:path;size:500;not null;"`
	Content   string     `gorm:"column:content;type:text;not null"`
	Hash      string     `gorm:"column:hash;size:64;not null"` // SHA256 of the PLAINTEXT: drift is compared against what the agent reports
	// ContentEnc marks how Content is stored: 0 = legacy plaintext, 1 = AES-GCM.
	// A column, not a byte prefix — a tracked file can legitimately begin with
	// the same marker the command payloads use, and sniffing would then skip it
	// in the backfill and fail to decrypt it on read.
	ContentEnc   int       `gorm:"column:content_enc;not null;default:0"`
	Version      int       `gorm:"column:version;not null;"`
	Status       string    `gorm:"column:status;size:20;not null;default:'applied'"`
	ErrorMessage *string   `gorm:"column:error_message;type:text"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt    time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (NodeFileModel) TableName() string {
	return "node_files"
}

// ToDomain converts the SQL model to a domain model
func (m *NodeFileModel) ToDomain() *node_file.NodeFile {
	return &node_file.NodeFile{
		ID:           m.ID,
		NodeID:       m.NodeID,
		Path:         m.Path,
		Content:      m.Content,
		Hash:         m.Hash,
		Version:      m.Version,
		Status:       m.Status,
		ErrorMessage: m.ErrorMessage,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

// FromDomain updates the SQL model from a domain model
func (m *NodeFileModel) FromDomain(file *node_file.NodeFile) {
	m.ID = file.ID
	m.NodeID = file.NodeID
	m.Path = file.Path
	m.Content = file.Content
	m.Hash = file.Hash
	m.Version = file.Version
	m.Status = file.Status
	m.ErrorMessage = file.ErrorMessage
	m.CreatedAt = file.CreatedAt
	m.UpdatedAt = file.UpdatedAt
}
