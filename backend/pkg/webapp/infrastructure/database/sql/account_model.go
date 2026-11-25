package sql

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
)

// AccountModel is the SQL representation of an account
type AccountModel struct {
	ID                  uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	Name                string    `gorm:"column:name;size:255;not null"`
	RequireTwoFactor    bool      `gorm:"column:require_two_factor;not null;default:false"`
	IdentifyDurationSec int       `gorm:"column:identify_duration_sec;not null;default:300"`
	// AuditRetentionDays overrides the install's audit retention for this
	// account: NULL = the install default, 0 = forever. Written only by
	// instance administration outside this API, never by this backend —
	// AccountRepository.Update omits it. The CHECK is the bound on those
	// writes; the floor is applied when it is read
	// (audit_event.EffectiveRetentionDays).
	AuditRetentionDays *int `gorm:"column:audit_retention_days;check:chk_accounts_audit_retention_days,audit_retention_days IS NULL OR audit_retention_days BETWEEN 0 AND 3650"`
	// OpenSignupPendingSince marks an open-registration sign-up that has not
	// been used yet (account.Account). Nullable with no default, so the
	// migration adds it as NULL on every existing row: every account that
	// existed before it is settled.
	OpenSignupPendingSince *time.Time `gorm:"column:open_signup_pending_since"`
	// DeviceLimit and DeviceLimitUntil are an operator's override of the
	// per-account device limit (ACCOUNT_DEVICE_LIMIT) for this account:
	// DeviceLimit NULL = the install default, 0 = no per-account limit, N =
	// at most N admitted devices; DeviceLimitUntil NULL = no end, otherwise
	// the override is in force only before that instant and the account
	// returns to the default after it (entitlement.ResolveAccountLimit).
	// Written only by instance administration outside this API, never by
	// this backend — AccountRepository.Update omits both. Nullable with no
	// default, so the migration adds them as NULL on every existing row.
	DeviceLimit      *int       `gorm:"column:device_limit;check:chk_accounts_device_limit,device_limit IS NULL OR device_limit >= 0"`
	DeviceLimitUntil *time.Time `gorm:"column:device_limit_until"`
	CreatedAt        time.Time  `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (AccountModel) TableName() string {
	return "accounts"
}

// ToDomain converts the SQL model to a domain model
func (m *AccountModel) ToDomain() *account.Account {
	return &account.Account{
		ID:                     m.ID,
		Name:                   m.Name,
		RequireTwoFactor:       m.RequireTwoFactor,
		IdentifyDurationSec:    m.IdentifyDurationSec,
		AuditRetentionDays:     m.AuditRetentionDays,
		OpenSignupPendingSince: m.OpenSignupPendingSince,
		CreatedAt:              m.CreatedAt,
		UpdatedAt:              m.UpdatedAt,
	}
}

// FromDomain updates the SQL model from a domain model
func (m *AccountModel) FromDomain(a *account.Account) {
	m.ID = a.ID
	m.Name = a.Name
	m.RequireTwoFactor = a.RequireTwoFactor
	m.IdentifyDurationSec = a.IdentifyDurationSec
	m.AuditRetentionDays = a.AuditRetentionDays
	m.OpenSignupPendingSince = a.OpenSignupPendingSince
	m.CreatedAt = a.CreatedAt
	m.UpdatedAt = a.UpdatedAt
}
