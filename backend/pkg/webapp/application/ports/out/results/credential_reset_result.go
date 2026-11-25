package results

import "github.com/google/uuid"

// CredentialResetResult is what an administrative credential reset did.
// TemporaryPassword is set only when the service generated the password; the
// command line prints it once and nothing keeps it.
type CredentialResetResult struct {
	UserID            uuid.UUID
	TemporaryPassword string
	// SessionsRevoked is false when the credential changed but revoking the
	// stored sessions failed; the advanced credential generation already
	// refuses every one of them at refresh.
	SessionsRevoked bool
	// AuditedAccounts is how many accounts the event was recorded under —
	// one per membership, active or disabled; zero for an identity that
	// belongs to none, or when the memberships could not be read.
	AuditedAccounts int
	// MembershipsUnreadable is true when AuditedAccounts is zero because the
	// memberships could not be listed, not because there are none.
	MembershipsUnreadable bool
}
