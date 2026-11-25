package repository

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/email_verification"
	"github.com/google/uuid"
)

// EmailVerificationRepository persists pending proof-of-mailbox tokens.
type EmailVerificationRepository interface {
	// Create stores a freshly minted verification.
	Create(ctx context.Context, verification *email_verification.EmailVerification) error
	// ClaimByHash atomically consumes an unused, unexpired token and returns
	// it. TTL and single-use are enforced by the UPDATE itself, so concurrent
	// clicks cannot both succeed. Returns ErrEmailVerificationInvalid for
	// unknown, expired AND already-used tokens alike — the endpoint is public
	// and must not be an oracle.
	ClaimByHash(ctx context.Context, tokenHash string) (*email_verification.EmailVerification, error)
	// RevokeUnusedByUserID marks a user's outstanding links spent, so a resend
	// or an email change invalidates the previous one.
	//
	// It marks rather than deletes because CountRecentByUserID reads the same
	// table: a revoke that removed the row would erase the very send it is
	// bounding, and the throttle below could never count past one.
	RevokeUnusedByUserID(ctx context.Context, userID uuid.UUID) error
	// CountRecentByUserID counts verifications minted for a user since the
	// given time — the input to the resend throttle.
	CountRecentByUserID(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error)
	// DeleteExpired prunes spent and expired rows, returning how many went
	// (the periodic sweep in main.go logs a nonzero count).
	DeleteExpired(ctx context.Context) (int64, error)
}
