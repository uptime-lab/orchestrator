package sql

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// NodeTrackedFileModel is the SQL representation of a tracked file.
type NodeTrackedFileModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
	NodeID    uuid.UUID `gorm:"column:node_id;type:uuid;index:idx_tracked_files_node_path,unique,priority:1;not null"`
	Path      string    `gorm:"column:path;size:500;index:idx_tracked_files_node_path,unique,priority:2;not null"`
	Source    string    `gorm:"column:source;size:16;not null;default:'user'"`

	Mode  string `gorm:"column:mode;size:8"`
	Owner string `gorm:"column:owner;size:64"`
	Group string `gorm:"column:group_name;size:64"`
	Size  int64  `gorm:"column:size;not null;default:0"`

	LastReadAt    *time.Time `gorm:"column:last_read_at"`
	LastAppliedAt *time.Time `gorm:"column:last_applied_at"`
	CreatedBy     uuid.UUID  `gorm:"column:created_by;type:uuid"`
	CreatedAt     time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model.
func (NodeTrackedFileModel) TableName() string {
	return "node_tracked_files"
}

// ToDomain converts the SQL model to a domain model.
func (m *NodeTrackedFileModel) ToDomain() *node_file.TrackedFile {
	return &node_file.TrackedFile{
		ID:            m.ID,
		AccountID:     m.AccountID,
		NodeID:        m.NodeID,
		Path:          m.Path,
		Source:        m.Source,
		Mode:          m.Mode,
		Owner:         m.Owner,
		Group:         m.Group,
		Size:          m.Size,
		LastReadAt:    m.LastReadAt,
		LastAppliedAt: m.LastAppliedAt,
		CreatedBy:     m.CreatedBy,
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

// FromDomain updates the SQL model from a domain model.
func (m *NodeTrackedFileModel) FromDomain(f *node_file.TrackedFile) {
	m.ID = f.ID
	m.AccountID = f.AccountID
	m.NodeID = f.NodeID
	m.Path = f.Path
	m.Source = f.Source
	m.Mode = f.Mode
	m.Owner = f.Owner
	m.Group = f.Group
	m.Size = f.Size
	m.LastReadAt = f.LastReadAt
	m.LastAppliedAt = f.LastAppliedAt
	m.CreatedBy = f.CreatedBy
	m.CreatedAt = f.CreatedAt
	m.UpdatedAt = f.UpdatedAt
}
