package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/google/uuid"
)

// AccountInviteRepository persists account invites.
type AccountInviteRepository interface {
	// Create inserts an invite. A pending invite for the same (account,
	// email) must be revoked first (partial unique index).
	Create(ctx context.Context, invite *account_invite.AccountInvite) error
	// GetByTokenHash returns the invite with the given token hash.
	GetByTokenHash(ctx context.Context, tokenHash string) (*account_invite.AccountInvite, error)
	// GetPendingByEmail returns one unaccepted, unexpired invite for the email
	// (any account), used by OIDC login to provision an invited user without a
	// password. Returns a not-found error when there is none.
	GetPendingByEmail(ctx context.Context, email string) (*account_invite.AccountInvite, error)
	// GetByID returns one invite within an account (used by resend, which
	// re-mints a token for an existing pending invite).
	GetByID(ctx context.Context, accountID, inviteID uuid.UUID) (*account_invite.AccountInvite, error)
	// ListPendingByAccountID returns unaccepted, unexpired invites.
	ListPendingByAccountID(ctx context.Context, accountID uuid.UUID) ([]account_invite.AccountInvite, error)
	// Accept marks the invite accepted if and only if it is still pending and
	// unexpired (atomic single-use); returns false when no row qualified.
	Accept(ctx context.Context, tokenHash string) (bool, error)
	// Delete removes an invite by id within an account (revocation;
	// pre-acceptance only — accepted invites are kept as a record).
	Delete(ctx context.Context, accountID, inviteID uuid.UUID) error
	// DeletePendingByEmail revokes a pending invite for an email (re-invite).
	DeletePendingByEmail(ctx context.Context, accountID uuid.UUID, email string) error
	// DeleteByOUID removes pending invites scoped to an OU (OU deletion).
	DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error
	// DeleteByAccountID removes all invites of an account.
	DeleteByAccountID(ctx context.Context, accountID uuid.UUID) error
}
