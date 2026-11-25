package repository

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/password_reset"
	"github.com/google/uuid"
)

// PasswordResetRepository persists pending password-reset grants.
type PasswordResetRepository interface {
	// Create stores a freshly minted reset grant.
	Create(ctx context.Context, reset *password_reset.PasswordReset) error
	// ClaimByHash atomically consumes an unused, unexpired token and returns
	// it. TTL and single-use live in the UPDATE's WHERE clause, so two
	// concurrent submissions cannot both reset the password. Returns
	// ErrPasswordResetInvalid for unknown, expired AND already-used tokens
	// alike — the endpoint is public and must not be an oracle.
	ClaimByHash(ctx context.Context, tokenHash string) (*password_reset.PasswordReset, error)
	// RevokeUnusedByUserID marks a user's outstanding grants spent, so
	// requesting a new link invalidates the previous one and a completed reset
	// leaves no live link behind.
	//
	// It marks rather than deletes because CountRecentByUserID reads the same
	// table: a revoke that removed the row would erase the very send it is
	// bounding, and the throttle below could never count past one.
	RevokeUnusedByUserID(ctx context.Context, userID uuid.UUID) error
	// CountRecentByUserID counts grants minted for a user since the given time
	// — the input to the per-user send throttle.
	CountRecentByUserID(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error)
	// DeleteExpired prunes spent and expired rows.
	DeleteExpired(ctx context.Context) error
}
