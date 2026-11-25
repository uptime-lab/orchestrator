package account

import (
	"time"

	"github.com/google/uuid"
)

// Account represents an account in the system
type Account struct {
	ID   uuid.UUID
	Name string
	// RequireTwoFactor makes a confirmed second factor mandatory for every
	// member. Enforcement is server-side: a member without one can reach the
	// enrollment endpoints and nothing else, so turning this on cannot lock
	// existing members out — it funnels them into enrolling.
	RequireTwoFactor bool
	// IdentifyDurationSec is how long an identify blink runs when the caller
	// names no duration — UI dispatches and physical button presses alike.
	// 0 = the default (300 s / 5 min).
	IdentifyDurationSec int
	// AuditRetentionDays is this account's own audit-trail retention, nil
	// for the install default (audit_event.EffectiveRetentionDays). Set by
	// instance administration outside this API; nothing here writes it — the
	// repository's Update leaves the column alone.
	AuditRetentionDays *int
	// OpenSignupPendingSince is set when the account was created through open
	// registration and no request has yet been admitted for it; nil for every
	// other account, and for every account that existed before the field.
	// While set, the account is usable for nothing but confirming its owner's
	// address (the open-signup gate), does not count toward the account
	// ceiling, and counts toward the cap on pending sign-ups until it is
	// older than the pending window. It is cleared exactly once, under the
	// ceiling, when its verified owner first uses it; the repository's Update
	// never writes it.
	OpenSignupPendingSince *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// NewAccount creates a new Account instance with default values
func NewAccount(name string) *Account {
	now := time.Now()
	return &Account{
		ID:        uuid.New(),
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
}
