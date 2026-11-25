package sql

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
)

type MasterTokenModel struct {
	ID              uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID       uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
	Name            string    `gorm:"column:name;size:255;not null"`
	TokenHash       string    `gorm:"column:token_hash;size:64;uniqueIndex;not null"`
	IsActive        bool      `gorm:"column:is_active;not null;default:true"`
	DefaultUsername string    `gorm:"column:default_username;size:255;not null;default:''"`
	DefaultPassword string    `gorm:"column:default_password;type:text;not null;default:''"`
	// DefaultPasswordEnc marks how DefaultPassword is stored: 0 = legacy
	// plaintext, 1 = AES-GCM. A column, not a byte prefix — every key writes the
	// same "v1:" marker, so the prefix says nothing about the key, and a legacy
	// plaintext password that happens to start with it would be misread as
	// ciphertext. Same reasoning as node_files.content_enc.
	DefaultPasswordEnc int        `gorm:"column:default_password_enc;not null;default:0"`
	SSHKeyID           *uuid.UUID `gorm:"column:ssh_key_id;type:uuid"`
	OUID               *uuid.UUID `gorm:"column:ou_id;type:uuid"`
	Tags               textArray  `gorm:"column:tags;type:text[]"`
	ExpiresAt          *time.Time `gorm:"column:expires_at"`
	LastUsedAt         *time.Time `gorm:"column:last_used_at"`
	CreatedAt          time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt          time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

func (MasterTokenModel) TableName() string {
	return "master_tokens"
}

func (m *MasterTokenModel) ToDomain() *master_token.MasterToken {
	return &master_token.MasterToken{
		ID:              m.ID,
		AccountID:       m.AccountID,
		Name:            m.Name,
		TokenHash:       m.TokenHash,
		IsActive:        m.IsActive,
		DefaultUsername: m.DefaultUsername,
		DefaultPassword: m.DefaultPassword,
		SSHKeyID:        m.SSHKeyID,
		OUID:            m.OUID,
		Tags:            []string(m.Tags),
		ExpiresAt:       m.ExpiresAt,
		LastUsedAt:      m.LastUsedAt,
		CreatedAt:       m.CreatedAt,
		UpdatedAt:       m.UpdatedAt,
	}
}

func (m *MasterTokenModel) FromDomain(d *master_token.MasterToken) {
	m.ID = d.ID
	m.AccountID = d.AccountID
	m.Name = d.Name
	m.TokenHash = d.TokenHash
	m.IsActive = d.IsActive
	m.DefaultUsername = d.DefaultUsername
	m.DefaultPassword = d.DefaultPassword
	m.SSHKeyID = d.SSHKeyID
	m.OUID = d.OUID
	m.Tags = textArray(d.Tags)
	m.ExpiresAt = d.ExpiresAt
	m.LastUsedAt = d.LastUsedAt
	m.CreatedAt = d.CreatedAt
	m.UpdatedAt = d.UpdatedAt
}
