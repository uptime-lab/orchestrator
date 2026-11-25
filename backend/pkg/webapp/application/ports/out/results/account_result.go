package results

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
)

// AccountResult represents an account in API responses
type AccountResult struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	// RequireTwoFactor drives the Team page's policy toggle and lets the client
	// explain WHY it is being pushed into enrollment.
	RequireTwoFactor bool `json:"require_two_factor"`
	// IdentifyDurationSec: how long identify blinks run account-wide, for UI
	// dispatches and physical button presses alike.
	IdentifyDurationSec int `json:"identify_duration_sec"`
	// AuditRetentionDays is how long this account's audit trail is kept, in
	// days; 0 = kept indefinitely. Read-only: it is the account's own value
	// when instance administration set one, else the install default, held
	// between the floor and the ceiling. Nothing in this API writes it.
	AuditRetentionDays int       `json:"audit_retention_days"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// NewAccountResult creates a new AccountResult from a domain account.
// installAuditRetentionDays is the install's default audit retention
// (AUDIT_EVENT_RETENTION_DAYS).
func NewAccountResult(account *account.Account, installAuditRetentionDays int) *AccountResult {
	dur := account.IdentifyDurationSec
	if dur <= 0 {
		dur = 300
	}
	return &AccountResult{
		ID:                  account.ID,
		Name:                account.Name,
		RequireTwoFactor:    account.RequireTwoFactor,
		IdentifyDurationSec: dur,
		AuditRetentionDays:  audit_event.EffectiveRetentionDays(account.AuditRetentionDays, installAuditRetentionDays),
		CreatedAt:           account.CreatedAt,
		UpdatedAt:           account.UpdatedAt,
	}
}
