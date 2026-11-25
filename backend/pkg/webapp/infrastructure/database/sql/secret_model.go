package sql

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/google/uuid"
)

// SecretModel is the GORM row for a secret. The value is stored encrypted in the
// value_encrypted column; the plaintext value is never persisted.
type SecretModel struct {
	ID             uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID      uuid.UUID `gorm:"column:account_id;type:uuid;index;uniqueIndex:uq_secrets_account_name;not null"`
	Name           string    `gorm:"column:name;size:255;uniqueIndex:uq_secrets_account_name;not null"`
	Kind           string    `gorm:"column:kind;size:32;not null"`
	Key            string    `gorm:"column:key;size:255"`
	ValueEncrypted string    `gorm:"column:value_encrypted;type:text"`
	// default:true so pre-existing rows stay write-only when the column is
	// added. A *bool (not bool) so GORM includes an explicit false on INSERT —
	// a plain false is a zero value and would be dropped in favor of the
	// column default.
	Sensitive  *bool      `gorm:"column:sensitive;not null;default:true"`
	Tags       textArray  `gorm:"column:tags;type:text[]"`
	CreatedAt  time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;not null;default:now()"`
	LastUsedAt *time.Time `gorm:"column:last_used_at"`
}

func (SecretModel) TableName() string {
	return "secrets"
}

// ToDomain maps the row to a domain secret. The value stays encrypted here; the
// repository decrypts value_encrypted into Secret.Value.
func (m *SecretModel) ToDomain() *secret.Secret {
	return &secret.Secret{
		ID:        m.ID,
		AccountID: m.AccountID,
		Name:      m.Name,
		Kind:      secret.SecretKind(m.Kind),
		Key:       m.Key,
		// nil cannot happen (NOT NULL column); treat it as sensitive anyway.
		Sensitive:  m.Sensitive == nil || *m.Sensitive,
		Tags:       []string(m.Tags),
		CreatedAt:  m.CreatedAt,
		UpdatedAt:  m.UpdatedAt,
		LastUsedAt: m.LastUsedAt,
	}
}

// FromDomain copies non-secret fields from the domain object. The repository
// sets ValueEncrypted separately after encrypting Secret.Value.
func (m *SecretModel) FromDomain(d *secret.Secret) {
	m.ID = d.ID
	m.AccountID = d.AccountID
	m.Name = d.Name
	m.Kind = string(d.Kind)
	m.Key = d.Key
	sensitive := d.Sensitive
	m.Sensitive = &sensitive
	m.Tags = textArray(d.Tags)
	m.CreatedAt = d.CreatedAt
	m.UpdatedAt = d.UpdatedAt
	m.LastUsedAt = d.LastUsedAt
}
