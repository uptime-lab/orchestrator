package entitlement

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// The per-account device limit is an operator's setting, independent of and
// additional to the instance-wide cap: when it is on, every account may hold
// at most that many ADMITTED devices (the same predicate the instance cap
// counts), and an admission must pass both. It bounds admission only, exactly
// like the instance cap — a device already admitted is never locked out,
// disconnected or refused a command, and an account that holds more than its
// limit keeps every device it has.

// AccountDeviceLimitErrorCode is the machine-readable code of the HTTP
// refusal at an account's own limit. Distinct from DeviceCapErrorCode: the
// remedy is the account's (write to the contact), not the install's.
const AccountDeviceLimitErrorCode = "account_device_limit_reached"

// GRPCAccountLimitMessage is the FIXED status message a device receives when
// its admission is refused at its account's limit. It must never equal
// GRPCUnverifiedMessage: an agent matches the limit messages exactly to hold
// for its long retry, and retries every other message through its ordinary
// backoff.
const GRPCAccountLimitMessage = "account device limit reached"

// MaxAccountLimitContactLength bounds the contact an operator configures. It
// is shown to every tenant on its subscription page and in every refusal, so
// it is one short line: an e-mail address fits in 254 characters.
const MaxAccountLimitContactLength = 254

// ValidAccountLimitContact reports whether s is usable as the contact: one
// line, no control characters, not longer than MaxAccountLimitContactLength.
// An empty value is valid (no contact configured).
func ValidAccountLimitContact(s string) bool {
	if len(s) > MaxAccountLimitContactLength {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return strings.TrimSpace(s) == s
}

// AccountLimitPlan names what an account's limit comes from, in the words its
// subscription page uses.
type AccountLimitPlan string

const (
	// AccountLimitPlanFree: the install's default limit applies — no
	// override, or an override whose end has passed.
	AccountLimitPlanFree AccountLimitPlan = "free"
	// AccountLimitPlanSubscription: an operator-written override is in force.
	AccountLimitPlanSubscription AccountLimitPlan = "subscription"
)

// AccountLimitOverride is what an operator wrote on one account: a limit
// (nil = none, the install default applies; 0 = no per-account limit for
// this account) and when it ends (nil = no end). The end has no meaning
// without a limit.
type AccountLimitOverride struct {
	Limit *int
	Until *time.Time
}

// AccountLimit is the per-account limit in force for one account at one
// instant.
type AccountLimit struct {
	// Limit is the account's limit right now; 0 means no per-account limit.
	Limit int
	// Plan is free (the install default) or subscription (an override).
	Plan AccountLimitPlan
	// Until is when the override in force ends; nil for the free plan and for
	// an override with no end.
	Until *time.Time
	// Contact is where to ask for more devices.
	Contact string
}

// Applies reports whether a per-account limit bounds this account's
// admissions.
func (l AccountLimit) Applies() bool { return l.Limit > 0 }

// ResolveAccountLimit is the one rule: an override is in force while it has
// a limit that is not negative and its end is unset or still ahead of now
// (no grace — at the end it is simply gone); otherwise the install default
// applies. A negative override (which the column's CHECK refuses) is read as
// no override.
func ResolveAccountLimit(defaultLimit int, o AccountLimitOverride, now time.Time) AccountLimit {
	if o.Limit != nil && *o.Limit >= 0 && (o.Until == nil || now.Before(*o.Until)) {
		out := AccountLimit{Limit: *o.Limit, Plan: AccountLimitPlanSubscription}
		if o.Until != nil {
			until := *o.Until
			out.Until = &until
		}
		return out
	}
	if defaultLimit < 0 {
		defaultLimit = 0
	}
	return AccountLimit{Limit: defaultLimit, Plan: AccountLimitPlanFree}
}

// AccountDeviceLimitError is the admission refusal at an account's own
// limit. Every number in it is the account's own — its limit and its
// admitted count — so it is safe to show to that account in full; it never
// carries the install-wide count.
type AccountDeviceLimitError struct {
	AccountID uuid.UUID
	Limit     int
	// Count is the account's admitted count, read under the admission lock.
	Count   int64
	Plan    AccountLimitPlan
	Until   *time.Time
	Contact string
}

func (e *AccountDeviceLimitError) Error() string {
	msg := fmt.Sprintf("This account already manages %d devices and its limit is %d. "+
		"Devices already added keep working", e.Count, e.Limit)
	if e.Contact != "" {
		return msg + fmt.Sprintf("; to add more, write to %s.", e.Contact)
	}
	return msg + "; to add more, ask the operator of this service."
}

// The request is well-formed; the account's limit refuses it, and the next
// step is to remove a device or ask for a higher limit — never to retry.
func (e *AccountDeviceLimitError) HTTPStatus() int   { return http.StatusConflict }
func (e *AccountDeviceLimitError) ErrorCode() string { return AccountDeviceLimitErrorCode }

// ErrorDetails is the structured half: `limit`, `account_devices`, `plan`
// (free | subscription), `until` (a dated subscription only) and `contact`
// (when configured). A field-name contract with the SPA.
func (e *AccountDeviceLimitError) ErrorDetails() any {
	details := map[string]any{
		"limit":           e.Limit,
		"account_devices": e.Count,
		"plan":            string(e.Plan),
	}
	if e.Until != nil {
		details["until"] = e.Until.UTC().Format(time.RFC3339)
	}
	if e.Contact != "" {
		details["contact"] = e.Contact
	}
	return details
}
