package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/two_factor"
)

// TwoFactorRepository persists users' second factors.
type TwoFactorRepository interface {
	// GetByUserID returns the user's factor, or ErrTwoFactorNotEnrolled when
	// there is none.
	GetByUserID(ctx context.Context, userID uuid.UUID) (*two_factor.TwoFactor, error)
	// Upsert stores a factor, replacing any pending enrollment for the user.
	Upsert(ctx context.Context, factor *two_factor.TwoFactor) error
	// CreatePending stores a new PENDING factor: an insert, or a replacement
	// of an existing pending one. It never touches an enabled factor —
	// ErrTwoFactorAlreadyEnabled — decided in the statement, not in a read
	// before it.
	CreatePending(ctx context.Context, factor *two_factor.TwoFactor) error
	// ConfirmPending enables exactly the pending factor whose secret is
	// expectedSecret, writing the confirmation and the recovery codes in one
	// conditional statement; a second confirmation, or one for a secret that
	// was since replaced, is ErrTwoFactorAlreadyEnabled and writes nothing.
	ConfirmPending(ctx context.Context, factor *two_factor.TwoFactor, expectedSecret string) error
	// ConfirmPendingWhileSessionLive is ConfirmPending under the session
	// boundary OIDC linking and passkey registration use:
	// the user's row lock, the credential-epoch test on (issuedAt,
	// generation) and the live-family test on familyID run first, then the
	// SAME conditional statement as ConfirmPending — so a factor cannot be
	// enabled by a session that was signed out, or a token minted before a
	// recovery, between the middleware's check and the write. ErrTokenInvalid
	// for a dead session; ConfirmPending's own refusals otherwise.
	ConfirmPendingWhileSessionLive(ctx context.Context, factor *two_factor.TwoFactor, expectedSecret string, familyID uuid.UUID, issuedAt time.Time, generation int64) error
	// ConsumeRecoveryCode atomically removes one code hash and reports whether
	// it was there. A conditional UPDATE, not a read-modify-write: rewriting
	// the whole array from a stale read lets two concurrent logins each spend
	// the same code and put it back.
	ConsumeRecoveryCode(ctx context.Context, userID uuid.UUID, codeHash string) (bool, error)
	// ConsumeTOTPStep records a TOTP time step as spent and reports whether it
	// was accepted (false = replay). Same conditional-UPDATE shape as
	// ConsumeRecoveryCode, and for the same reason.
	ConsumeTOTPStep(ctx context.Context, userID uuid.UUID, step int64) (bool, error)
	// RecordTOTPStep stores a matched step without refusing a replay. For
	// enrolment confirmation, which validates against the pending secret.
	RecordTOTPStep(ctx context.Context, userID uuid.UUID, step int64) error
	// Delete removes the user's factor entirely (disabling 2FA).
	Delete(ctx context.Context, userID uuid.UUID) error
	// RemoveEnabledAndAdvanceGeneration is the administrative reset: under
	// the user's row lock it deletes an ENABLED factor (its secret and its
	// recovery codes live on the same row) and advances
	// users.credential_generation in the same transaction, because a change
	// to how a user signs in moves the credential epoch. Conditional in the
	// statement: a pending enrolment is not a factor and is left alone, and
	// with no enabled factor nothing is written and removed is false.
	RemoveEnabledAndAdvanceGeneration(ctx context.Context, userID uuid.UUID) (removed bool, err error)
}
