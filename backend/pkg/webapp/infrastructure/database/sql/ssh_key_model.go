package sql

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/google/uuid"
)

// SSHKeyModel is the SQL representation of an SSH key
type SSHKeyModel struct {
	ID          uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID   uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
	Alias       string    `gorm:"column:alias;size:255;not null"`
	KeyType     string    `gorm:"column:key_type;size:20;not null;default:ssh"`
	KeyContent  string    `gorm:"column:key_content;type:text;not null"`
	Fingerprint string    `gorm:"column:fingerprint;size:100;not null"`
	IsDefault   bool      `gorm:"column:is_default;default:false;not null"`
	Tags        textArray `gorm:"column:tags;type:text[]"`
	// PrivateKeyEncrypted holds the AES-256-GCM ciphertext of a server-generated
	// connection keypair's private key (empty for uploaded public-only keys).
	PrivateKeyEncrypted string    `gorm:"column:private_key_encrypted;type:text"`
	CreatedAt           time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt           time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (SSHKeyModel) TableName() string {
	return "ssh_keys"
}

// ToDomain converts the SQL model to a domain model
func (m *SSHKeyModel) ToDomain() *ssh_key.SSHKey {
	keyType := ssh_key.KeyType(m.KeyType)
	if keyType == "" {
		keyType = ssh_key.KeyTypeSSH // Default for backward compatibility
	}
	return &ssh_key.SSHKey{
		ID:          m.ID,
		AccountID:   m.AccountID,
		Alias:       m.Alias,
		KeyType:     keyType,
		KeyContent:  m.KeyContent,
		Fingerprint: m.Fingerprint,
		IsDefault:   m.IsDefault,
		Tags:        []string(m.Tags),
		// PrivateKey is left empty here — decryption happens in the repository
		// (which holds the encryptor) via GetWithPrivateKey. HasPrivateKey only
		// reflects presence so the API can flag connection keys cheaply.
		HasPrivateKey: m.PrivateKeyEncrypted != "",
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

// FromDomain updates the SQL model from a domain model
func (m *SSHKeyModel) FromDomain(key *ssh_key.SSHKey) {
	m.ID = key.ID
	m.AccountID = key.AccountID
	m.Alias = key.Alias
	m.KeyType = string(key.KeyType)
	m.KeyContent = key.KeyContent
	m.Fingerprint = key.Fingerprint
	m.IsDefault = key.IsDefault
	m.Tags = textArray(key.Tags)
	m.CreatedAt = key.CreatedAt
	m.UpdatedAt = key.UpdatedAt
}
