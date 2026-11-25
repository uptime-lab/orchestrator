package sql

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/google/uuid"
)

// SecretAttachmentModel is the GORM row linking a subject (a secret — non-null
// secret_id — or a tag, non-empty tag) to a target (exactly one of ou_id /
// node_id non-null). Both invariants are enforced in the domain.
type SecretAttachmentModel struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID uuid.UUID  `gorm:"column:account_id;type:uuid;index;not null"`
	SecretID  *uuid.UUID `gorm:"column:secret_id;type:uuid;index"`
	Tag       string     `gorm:"column:tag;size:40;not null;default:'';index"`
	OUID      *uuid.UUID `gorm:"column:ou_id;type:uuid;index"`
	NodeID    *uuid.UUID `gorm:"column:node_id;type:uuid;index"`
}

func (SecretAttachmentModel) TableName() string {
	return "secret_attachments"
}

func (m *SecretAttachmentModel) ToDomain() *secret.SecretAttachment {
	return &secret.SecretAttachment{
		ID:        m.ID,
		AccountID: m.AccountID,
		SecretID:  m.SecretID,
		Tag:       m.Tag,
		OUID:      m.OUID,
		NodeID:    m.NodeID,
	}
}

func (m *SecretAttachmentModel) FromDomain(d *secret.SecretAttachment) {
	m.ID = d.ID
	m.AccountID = d.AccountID
	m.SecretID = d.SecretID
	m.Tag = d.Tag
	m.OUID = d.OUID
	m.NodeID = d.NodeID
}
