package audit_event

// Retention of the audit trail, in days. The install sets a default
// (AUDIT_EVENT_RETENTION_DAYS, a year unless set); an account may carry its own value, set by
// instance administration outside the tenant API (accounts.audit_retention_days,
// NULL = the install default). 0 keeps the trail forever, in both places.
const (
	// MinRetentionDays is the floor every positive setting is raised to.
	// Rate limits are counted off this table (MEMBER_MAX_INVITES_PER_DAY over
	// a rolling 24 hours), so a retention shorter than their window would
	// delete the rows that bound them and defeat the limit. The floor covers
	// the longest such window with room to spare; a reader with a longer one
	// must raise it (member.TestInviteWindowFitsTheAuditRetentionFloor).
	MinRetentionDays = 7
	// MaxRetentionDays is the ceiling a positive setting is lowered to, and
	// the bound the accounts column's CHECK constraint enforces on writes.
	MaxRetentionDays = 3650
)

// EffectiveRetentionDays is how long one account's trail is kept: its own
// value when it has one, else the install default, held between the floor
// and the ceiling. 0 is "forever" and stays 0. A negative account value is
// not a setting (the column's constraint refuses one) and falls back to the
// install default; a negative install default is treated as forever.
func EffectiveRetentionDays(accountDays *int, installDays int) int {
	days := installDays
	if accountDays != nil && *accountDays >= 0 {
		days = *accountDays
	}
	switch {
	case days <= 0:
		return 0
	case days < MinRetentionDays:
		return MinRetentionDays
	case days > MaxRetentionDays:
		return MaxRetentionDays
	default:
		return days
	}
}
