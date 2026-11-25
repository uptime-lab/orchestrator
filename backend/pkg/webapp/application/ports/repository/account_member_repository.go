package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/google/uuid"
)

// AccountMemberRepository persists account memberships (global user
// identities linked to accounts).
type AccountMemberRepository interface {
	// Create inserts a membership; inserting an existing (account, user) pair
	// is a no-op so backfill migrations can re-run safely.
	Create(ctx context.Context, member *account_member.AccountMember) error
	// GetByAccountAndUser returns the membership of a user in an account.
	GetByAccountAndUser(ctx context.Context, accountID, userID uuid.UUID) (*account_member.AccountMember, error)
	// ListByAccountID returns all memberships of an account.
	ListByAccountID(ctx context.Context, accountID uuid.UUID) ([]account_member.AccountMember, error)
	// CountByAccountID counts an account's memberships, for the seat cap.
	// Counts EVERY status, not just active: a suspended member still occupies a
	// seat and can be reactivated, so excluding them would let an account hold
	// unlimited seats by parking them.
	CountByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error)
	// ListByUserID returns all memberships of a user across accounts.
	ListByUserID(ctx context.Context, userID uuid.UUID) ([]account_member.AccountMember, error)
	// UpdateStatus sets the membership status.
	UpdateStatus(ctx context.Context, accountID, userID uuid.UUID, status account_member.MemberStatus) error
	// Delete removes a user's membership in an account.
	Delete(ctx context.Context, accountID, userID uuid.UUID) error
	// DeleteByAccountID removes all memberships of an account (account
	// deletion cleanup; never touches user rows).
	DeleteByAccountID(ctx context.Context, accountID uuid.UUID) error
}
