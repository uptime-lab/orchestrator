package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/google/uuid"
)

// RoleBindingRepository persists IAM-style role bindings.
type RoleBindingRepository interface {
	// Create inserts a binding; duplicates (per the partial unique indexes)
	// are a no-op so backfill migrations can re-run safely.
	Create(ctx context.Context, binding *role_binding.RoleBinding) error
	// ListByAccountAndUser returns a user's bindings within an account.
	ListByAccountAndUser(ctx context.Context, accountID, userID uuid.UUID) ([]role_binding.RoleBinding, error)
	// ListByAccountID returns all bindings of an account.
	ListByAccountID(ctx context.Context, accountID uuid.UUID) ([]role_binding.RoleBinding, error)
	// ReplaceForUser transactionally replaces a user's bindings in an account.
	ReplaceForUser(ctx context.Context, accountID, userID uuid.UUID, bindings []role_binding.RoleBinding) error
	// DeleteByAccountAndUser removes all of a user's bindings in an account.
	DeleteByAccountAndUser(ctx context.Context, accountID, userID uuid.UUID) error
	// DeleteByOUID removes all bindings scoped to an OU (OU deletion cleanup).
	DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error
	// DeleteByAccountID removes all bindings of an account.
	DeleteByAccountID(ctx context.Context, accountID uuid.UUID) error
	// NOTE: there is deliberately no CountActiveSuperAdmins here. One existed,
	// documented as what "the last-super_admin invariant reads", and had zero
	// callers — the real guard counts inside the mutation transaction, which is
	// the only place the count cannot go stale between check and write. A
	// plausible-looking counter next to the port is worse than none: it invites
	// exactly the check-then-act the invariant exists to prevent.
}
