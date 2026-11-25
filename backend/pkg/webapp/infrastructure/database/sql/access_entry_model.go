package sql

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/google/uuid"
)

// AccessEntryModel is the GORM row for a declarative access entry. Exactly one
// of ou_id / node_id is set, or neither (an account-level default). The
// password is stored encrypted (password_encrypted); the plaintext never
// touches the row. Principals (ca entries) are JSON-serialized.
type AccessEntryModel struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID uuid.UUID  `gorm:"column:account_id;type:uuid;index;not null"`
	Type      string     `gorm:"column:type;size:16;not null"`
	// OUID is the LEGACY single-unit column, superseded by the
	// access_entry_ous join table (an entry can now live in several units).
	// Kept only so Setup() can backfill the join table from pre-existing rows;
	// never written or read by the application after that.
	OUID   *uuid.UUID `gorm:"column:ou_id;type:uuid;index"`
	NodeID *uuid.UUID `gorm:"column:node_id;type:uuid;index"`
	// AccountWide is meaningful only when the entry has no OUs and no node:
	// true = account-level default (fleet-wide), false = unassigned (no effect).
	AccountWide bool `gorm:"column:account_wide;not null;default:false"`

	Username          string   `gorm:"column:username;size:32;not null;default:''"`
	Sudo              bool     `gorm:"column:sudo;not null;default:false"`
	PasswordEncrypted *string  `gorm:"column:password_encrypted"`
	AdoptUnmanaged    bool     `gorm:"column:adopt_unmanaged;not null;default:false"`
	Principals        []string `gorm:"column:principals;serializer:json"`

	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (AccessEntryModel) TableName() string { return "access_entries" }

// toDomain maps the row to a domain entity WITHOUT the password (decryption is
// the repository's job) and without KeyIDs/OUIDs (hydrated from join tables).
func (m *AccessEntryModel) toDomain() *access_entry.AccessEntry {
	return &access_entry.AccessEntry{
		ID:             m.ID,
		AccountID:      m.AccountID,
		Type:           access_entry.Type(m.Type),
		NodeID:         m.NodeID,
		AccountWide:    m.AccountWide,
		Username:       m.Username,
		Sudo:           m.Sudo,
		AdoptUnmanaged: m.AdoptUnmanaged,
		Principals:     m.Principals,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}

// fromDomain maps a domain entity to the row WITHOUT the password field, which
// the repository sets after encrypting. KeyIDs and OUIDs live in join tables.
func (m *AccessEntryModel) fromDomain(d *access_entry.AccessEntry) {
	m.ID = d.ID
	m.AccountID = d.AccountID
	m.Type = string(d.Type)
	m.NodeID = d.NodeID
	m.AccountWide = d.AccountWide
	m.Username = d.Username
	m.Sudo = d.Sudo
	m.AdoptUnmanaged = d.AdoptUnmanaged
	m.Principals = d.Principals
	m.CreatedAt = d.CreatedAt
	m.UpdatedAt = d.UpdatedAt
}

// AccessEntryKeyModel is the join row referencing an ssh_keys row from an
// access entry. account_id scopes deletes; the (access_entry_id, ssh_key_id)
// pair is the primary key so re-attaching is idempotent.
type AccessEntryKeyModel struct {
	AccessEntryID uuid.UUID `gorm:"column:access_entry_id;type:uuid;primaryKey;not null"`
	SSHKeyID      uuid.UUID `gorm:"column:ssh_key_id;type:uuid;primaryKey;not null"`
	AccountID     uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
}

func (AccessEntryKeyModel) TableName() string { return "access_entry_keys" }

// AccessEntryOUModel is the join row scoping an access entry to a unit. One
// entry may reference several units (one user in multiple OUs); the composite
// primary key makes re-assignment idempotent.
type AccessEntryOUModel struct {
	AccessEntryID uuid.UUID `gorm:"column:access_entry_id;type:uuid;primaryKey;not null"`
	OUID          uuid.UUID `gorm:"column:ou_id;type:uuid;primaryKey;not null;index"`
	AccountID     uuid.UUID `gorm:"column:account_id;type:uuid;index;not null"`
}

func (AccessEntryOUModel) TableName() string { return "access_entry_ous" }
