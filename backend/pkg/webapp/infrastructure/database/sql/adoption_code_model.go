package sql

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/adoption_code"
	"github.com/google/uuid"
)

// AdoptionCodeModel is the SQL representation of a one-time adoption code.
type AdoptionCodeModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
	// Partial-unique on the node while a code is still usable enforces
	// one-active-code-per-node at the DB level: a concurrent double re-mint
	// (both delete then both insert) is serialized here — the second insert
	// hits the unique violation instead of leaving two live codes.
	NodeID   uuid.UUID `gorm:"column:node_id;type:uuid;index;uniqueIndex:idx_adoption_codes_active_node,where:used_at IS NULL;not null"`
	CodeHash string    `gorm:"column:code_hash;size:64;uniqueIndex;not null"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null"`
	UsedAt    *time.Time `gorm:"column:used_at"`
	CreatedAt time.Time  `gorm:"column:created_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (AdoptionCodeModel) TableName() string {
	return "adoption_codes"
}

// ToDomain converts the SQL model to a domain model
func (m *AdoptionCodeModel) ToDomain() *adoption_code.AdoptionCode {
	return &adoption_code.AdoptionCode{
		ID:        m.ID,
		AccountID: m.AccountID,
		NodeID:    m.NodeID,
		CodeHash:  m.CodeHash,
		ExpiresAt: m.ExpiresAt,
		UsedAt:    m.UsedAt,
		CreatedAt: m.CreatedAt,
	}
}

// FromDomain updates the SQL model from a domain model
func (m *AdoptionCodeModel) FromDomain(c *adoption_code.AdoptionCode) {
	m.ID = c.ID
	m.AccountID = c.AccountID
	m.NodeID = c.NodeID
	m.CodeHash = c.CodeHash
	m.ExpiresAt = c.ExpiresAt
	m.UsedAt = c.UsedAt
	m.CreatedAt = c.CreatedAt
}
