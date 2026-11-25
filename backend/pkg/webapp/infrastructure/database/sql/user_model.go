package sql

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// UserModel is the SQL representation of a user (a global identity; account
// association lives in account_members). The legacy account_id column is
// deliberately absent from this struct: it survives in the DB only as the
// consumable "not yet migrated" marker read by the role-binding backfill, and
// keeping it out of the struct guarantees no GORM write path can touch it.
type UserModel struct {
	ID                uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	Name              string     `gorm:"column:name;size:255;not null"`
	Email             string     `gorm:"column:email;size:255;uniqueIndex;not null"`
	HashedPassword    string     `gorm:"column:hashed_password;size:255;not null"`
	PasswordChangedAt *time.Time `gorm:"column:password_changed_at"`
	// EmailVerifiedAt: proven mailbox ownership (nil = unverified). Gates
	// silent OIDC→local auto-linking.
	EmailVerifiedAt *time.Time `gorm:"column:email_verified_at"`
	// BetaNoticeAckAt / BetaNoticeAckVersion: accepted the operator's notice or
	// terms of use, and which revision (0 = never accepted anything). The
	// column names predate the terms wording and stay: existing rows use them.
	BetaNoticeAckAt      *time.Time `gorm:"column:beta_notice_ack_at"`
	BetaNoticeAckVersion int        `gorm:"column:beta_notice_ack_version;not null;default:0"`
	// CredentialGeneration: advanced by the repository's credential
	// transitions only (SetPassword, an email change in UpdateProfile).
	CredentialGeneration int64     `gorm:"column:credential_generation;not null;default:1"`
	CreatedAt            time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt            time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (UserModel) TableName() string {
	return "users"
}

// ToDomain converts the SQL model to a domain model
func (m *UserModel) ToDomain() *user.User {
	return &user.User{
		ID:                m.ID,
		Name:              m.Name,
		Email:             m.Email,
		HashedPassword:    m.HashedPassword,
		PasswordChangedAt: m.PasswordChangedAt,
		EmailVerifiedAt:   m.EmailVerifiedAt,

		BetaNoticeAckAt:      m.BetaNoticeAckAt,
		BetaNoticeAckVersion: m.BetaNoticeAckVersion,
		CredentialGeneration: m.CredentialGeneration,
		CreatedAt:            m.CreatedAt,
		UpdatedAt:            m.UpdatedAt,
	}
}

// FromDomain updates the SQL model from a domain model
func (m *UserModel) FromDomain(u *user.User) {
	m.ID = u.ID
	m.Name = u.Name
	m.Email = u.Email
	m.HashedPassword = u.HashedPassword
	m.PasswordChangedAt = u.PasswordChangedAt
	m.EmailVerifiedAt = u.EmailVerifiedAt
	m.BetaNoticeAckAt = u.BetaNoticeAckAt
	m.BetaNoticeAckVersion = u.BetaNoticeAckVersion
	m.CredentialGeneration = u.CredentialGeneration
	m.CreatedAt = u.CreatedAt
	m.UpdatedAt = u.UpdatedAt
}
