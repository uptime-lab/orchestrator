package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

// AccountDeviceLimitReader reads an account's override of the per-account
// device limit: the columns accounts.device_limit and
// accounts.device_limit_until, which instance administration writes outside
// this API and nothing in this backend writes. A zero override (both nil)
// means the install default applies.
type AccountDeviceLimitReader interface {
	DeviceLimitOverride(ctx context.Context, accountID uuid.UUID) (entitlement.AccountLimitOverride, error)
}

// AccountRepository defines the interface for account-related database operations
type AccountRepository interface {
	// Create creates a new account
	Create(ctx context.Context, account *account.Account) error

	// GetByID retrieves an account by its ID
	GetByID(ctx context.Context, id uuid.UUID) (*account.Account, error)

	// Update updates an existing account
	Update(ctx context.Context, account *account.Account) error

	// Delete deletes an account by its UUID
	Delete(ctx context.Context, id uuid.UUID) error

	// InstanceOperatorAccountID is the account that operates this install:
	// the oldest one, by created_at with the id as the tiebreak (the rule the
	// entitlement service uses; an out-of-band account purge must use the same
	// one). The
	// role is not stored, so deleting that account hands it to the next
	// oldest — a tenant. app_errors.ErrAccountNotFound when none exists.
	InstanceOperatorAccountID(ctx context.Context) (uuid.UUID, error)

	// Counts returns how many accounts exist on this instance, for the
	// self-registration ceiling. Counts ACCOUNTS rather than users on purpose:
	// an account is what carries a storage quota, and an invited teammate joins
	// an existing one rather than creating another. pendingSince is the start
	// of the pending window: an open sign-up still pending and created at or
	// after it counts as Pending.
	Counts(ctx context.Context, pendingSince time.Time) (AccountCounts, error)

	// SettleOpenSignup clears an account's OpenSignupPendingSince if admit
	// accepts the counts at that moment, in one transaction under the same
	// lock CreateAdmitted takes, so settling cannot carry the instance past
	// its ceiling any more than a sign-up can. It reports whether the account
	// is settled afterwards (true also when it already was); an error from
	// admit is returned unchanged and leaves the account pending.
	SettleOpenSignup(ctx context.Context, id uuid.UUID, pendingSince time.Time, admit AccountAdmission) (bool, error)

	// CreateAdmitted inserts the account only if admit accepts the count of
	// accounts that exist at that moment — count, decision and insert in ONE
	// transaction under a constant advisory lock, so N sign-ups arriving
	// together on an empty instance serialize instead of all reading zero
	// and all becoming "the first owner". The decision stays the caller's
	// (it is the registration policy); the serialization is the
	// repository's. A count that cannot be read is reported as
	// ErrAccountCountUnavailable and nothing is inserted: this one admission
	// fails CLOSED, because the count decides whether an invite code is
	// required at all. An error returned by admit is returned unchanged.
	//
	// admit may mark the candidate (OpenSignupPendingSince) before it is
	// inserted: the row written is the candidate as admit left it.
	CreateAdmitted(ctx context.Context, account *account.Account, pendingSince time.Time, admit AccountAdmission) error
}

// AccountCounts is what an admission decides on.
type AccountCounts struct {
	// Total is every account row, pending or not: an instance with none
	// belongs to its first owner.
	Total int64
	// Settled is every account that is not a pending open sign-up — what the
	// account ceiling (SERVER_MAX_ACCOUNTS) bounds.
	Settled int64
	// Pending is the pending open sign-ups created inside the pending window
	// — what the pending cap bounds. Older ones are left out, so abandoned
	// sign-ups cannot hold the cap shut.
	Pending int64
}

// AccountAdmission decides, given the counts, whether one more account may
// be created (or a pending one settled); nil admits. candidate is the account
// about to be written, which the admission may mark as a pending open
// sign-up. It is called inside the transaction that holds the lock, so it
// must not touch the database.
type AccountAdmission func(counts AccountCounts, candidate *account.Account) error

// ErrAccountCountUnavailable: the admission's count could not be read. The
// caller refuses the sign-up; see CreateAdmitted for why that is the safe
// direction here and not for the device quotas.
var ErrAccountCountUnavailable = errors.New("account count unavailable")
