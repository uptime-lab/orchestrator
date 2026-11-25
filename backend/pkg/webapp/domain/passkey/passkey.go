// Package passkey models WebAuthn credentials used as a full passwordless
// sign-in path.
//
// These are DISCOVERABLE credentials (resident keys): the authenticator stores
// the user handle itself, so the browser can offer an account without the site
// asking who is signing in. That is what makes "Sign in with a passkey" a
// complete replacement for email+password rather than a second factor bolted
// onto one — the authenticator already enforces user verification (PIN or
// biometric), so a passkey login is inherently two-factor.
//
// Nothing secret lives here. A credential is a PUBLIC key plus a handle; the
// private half never leaves the authenticator, which is precisely why this is
// worth having.
package passkey

import (
	"time"

	"github.com/google/uuid"
)

// Passkey is one registered authenticator credential.
type Passkey struct {
	ID     uuid.UUID
	UserID uuid.UUID
	// CredentialID is the authenticator's opaque handle for this credential.
	// Unique across all users: it is what an authentication ceremony presents.
	CredentialID []byte
	// PublicKey verifies the assertion signatures. Public by definition.
	PublicKey []byte
	// AAGUID identifies the authenticator model (YubiKey, iCloud Keychain, …).
	AAGUID []byte
	// SignCount is the authenticator's monotonic counter. A counter that goes
	// BACKWARDS is the documented signal of a cloned authenticator — see
	// CloneWarning handling in the service.
	SignCount uint32
	// BackupEligible / BackupState describe whether the credential syncs to a
	// cloud keychain. Worth recording: a synced passkey has a very different
	// recovery story from one bound to a single hardware key, and the UI says so.
	BackupEligible bool
	BackupState    bool
	// Name is a human label ("MacBook Touch ID"), so a user with several
	// passkeys can tell which is which before deleting one.
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// New records a freshly registered credential.
func New(userID uuid.UUID, credentialID, publicKey, aaguid []byte, signCount uint32, backupEligible, backupState bool, name string) *Passkey {
	return &Passkey{
		ID:             uuid.New(),
		UserID:         userID,
		CredentialID:   credentialID,
		PublicKey:      publicKey,
		AAGUID:         aaguid,
		SignCount:      signCount,
		BackupEligible: backupEligible,
		BackupState:    backupState,
		Name:           name,
		CreatedAt:      time.Now(),
	}
}

// MarkUsed stamps a successful authentication and advances the counter.
func (p *Passkey) MarkUsed(signCount uint32) {
	now := time.Now()
	p.LastUsedAt = &now
	p.SignCount = signCount
}
