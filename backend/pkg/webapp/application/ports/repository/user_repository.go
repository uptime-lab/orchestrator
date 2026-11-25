package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/user"
)

// ProfileChange reports what UpdateProfile's statement decided about the
// sign-in address, so the service can mail the verification and the notice to
// the PREVIOUS address without a second read that could name a different one.
type ProfileChange struct {
	// EmailChanged is true when the statement replaced the stored address
	// with a different one (a case-only change is not a change).
	EmailChanged bool
	// PreviousEmail is the address the row held before the write.
	PreviousEmail string
}

// UserRepository defines the interface for user-related database operations
type UserRepository interface {
	// Create creates a new user
	Create(ctx context.Context, user *user.User) error

	// GetByID retrieves a user by ID
	GetByID(ctx context.Context, id uuid.UUID) (*user.User, error)

	// GetByEmail retrieves a user by email address
	GetByEmail(ctx context.Context, email string) (*user.User, error)

	// ListByEmailIgnoringCase returns EVERY identity whose stored address
	// equals the normalized one case-insensitively, so a caller that must act
	// on exactly one person can refuse an ambiguous answer instead of
	// trusting GetByEmail's first row. The unique index on email is
	// byte-wise; boot-time normalization is what keeps it to one row, and
	// this read does not assume that it ran.
	ListByEmailIgnoringCase(ctx context.Context, email string) ([]*user.User, error)

	// Update writes EVERY column from the caller's copy. It exists for
	// registration-time shaping and tests; it must never carry a credential
	// or verification transition, because a copy read before a concurrent
	// password reset would write the old hash and the old
	// PasswordChangedAt back over the reset. Those go
	// through the column-specific methods below.
	Update(ctx context.Context, user *user.User) error
	// SetPassword writes only the password columns and advances the
	// credential generation in the same statement.
	SetPassword(ctx context.Context, id uuid.UUID, hashedPassword string, changedAt time.Time) error
	// SetPasswordIfEmail is SetPassword conditional on the stored sign-in
	// address still being expectedEmail, decided in the statement's WHERE:
	// a password-reset grant proves control of the mailbox
	// it was mailed to, and an email change that commits between the
	// service's address check and the write makes that mailbox a stranger's.
	// Answers ErrPasswordResetStale when the row exists under another
	// address and ErrUserNotFound when it does not exist.
	SetPasswordIfEmail(ctx context.Context, id uuid.UUID, hashedPassword, expectedEmail string, changedAt time.Time) error
	// UpdateProfile is a PARTIAL write: a nil name or email is left exactly
	// as stored, structurally — there is no value for a stale snapshot to
	// write back (a name-only update that read before a concurrent email
	// change used to restore the previous address). Whether the email
	// CHANGED is decided inside the statement
	// against the stored value, never by the caller; only then are the
	// verification cleared and the credential generation advanced, in the
	// same statement. The email is normalized before it is compared or
	// stored.
	UpdateProfile(ctx context.Context, id uuid.UUID, name, email *string) (ProfileChange, error)
	// SetPasswordWhileSessionLive and UpdateProfileWhileSessionLive are the
	// two writes above under the session-bound boundary every other
	// credential write already uses: under the
	// user's row lock the caller's session (familyID, issuedAt, generation)
	// must still be live and of the current credential epoch, or nothing is
	// written and ErrTokenInvalid is returned. The step-up guard proved that
	// session before the handler ran; a sign-out or a recovery landing
	// between the guard and the statement takes the same lock and therefore
	// either already ended the family or runs after the write. The
	// unbound SetPassword stays for password reset (a grant, not a session);
	// the unbound UpdateProfile is for fixtures and tests.
	SetPasswordWhileSessionLive(ctx context.Context, id uuid.UUID, hashedPassword string, changedAt time.Time, familyID uuid.UUID, issuedAt time.Time, generation int64) error
	UpdateProfileWhileSessionLive(ctx context.Context, id uuid.UUID, name, email *string, familyID uuid.UUID, issuedAt time.Time, generation int64) (ProfileChange, error)
	// MarkEmailVerified stamps the verification only while the stored
	// address still equals email and is not yet verified. false means
	// nothing was written — already verified, or the address moved since
	// the proof was made — and is not an error.
	MarkEmailVerified(ctx context.Context, id uuid.UUID, email string, at time.Time) (bool, error)
	// AcknowledgeBetaNotice writes the two terms-acceptance columns only.
	AcknowledgeBetaNotice(ctx context.Context, id uuid.UUID, version int, at time.Time) error

	// Delete deletes a user identity. Never used for account cleanup —
	// identities can belong to several accounts.
	Delete(ctx context.Context, id uuid.UUID) error

	// ListByAccountID retrieves an account's members (via account_members)
	// with pagination.
	ListByAccountID(ctx context.Context, accountID uuid.UUID, offset, limit int) ([]*user.User, int, error)
}
