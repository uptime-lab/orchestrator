package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/google/uuid"
)

// MemberMutationRepository performs the membership mutations that must not race
// each other. The last-super_admin invariant is check-then-act by nature: two
// concurrent demotions of the last two super_admins can each observe a count of
// two and both proceed, leaving the account with none. Every method here takes
// the account's advisory lock for the whole transaction and re-reads the
// active-super_admin count inside it, so the check and the mutation it guards
// are atomic.
//
// Note the lock is per account; any writer outside this API that mutates the
// same tables must take it too, or the invariant does not hold.
type MemberMutationRepository interface {
	// ReplaceBindingsGuarded replaces a member's bindings. Inside the same
	// transaction it re-checks that the WRITER still holds an active
	// account-wide super_admin binding (the only role that may grant roles, and
	// the highest one), refuses a stale precondition, enforces the invariant
	// when the new set no longer grants account-wide super_admin, and then
	// revokes the member's pending invitations, which only a super_admin may
	// have issued.
	ReplaceBindingsGuarded(ctx context.Context, accountID uuid.UUID, req BindingsReplace) error
	// SetStatusGuarded changes a member's status; the invariant is enforced
	// and the member's pending invitations revoked when the member is being
	// disabled.
	SetStatusGuarded(ctx context.Context, accountID, userID uuid.UUID, status account_member.MemberStatus) error
	// RemoveGuarded deletes a membership together with all of the member's
	// bindings and pending invitations in the account — atomically, so a
	// failure cannot leave a membership stripped of its bindings, and an
	// invitation cannot outlive the member who issued it.
	RemoveGuarded(ctx context.Context, accountID, userID uuid.UUID) error
}

// BindingsReplace is one guarded replace of a member's role bindings.
type BindingsReplace struct {
	// ActorID is the member performing the write; re-checked under the lock.
	ActorID uuid.UUID
	// UserID is the member whose bindings are replaced.
	UserID   uuid.UUID
	Bindings []role_binding.RoleBinding
	// KeepsSuperAdmin reports whether the new set still grants account-wide
	// super_admin; when it does not, the invariant is enforced.
	KeepsSuperAdmin bool
	// ExpectedBindingIDs, when non-nil, is the binding set the writer read.
	// Every replace mints new binding ids, so the set is the version of the
	// member's roles: a write prepared from an older read fails with
	// ErrMemberBindingsStale instead of restoring roles removed since.
	ExpectedBindingIDs *[]uuid.UUID
}
