package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.MemberMutationRepository = (*MemberMutationRepository)(nil)

// BindingsReplace is the repository port's request type, named here for brevity.
type BindingsReplace = repository.BindingsReplace

// MemberLockClass namespaces the membership advisory lock so it cannot collide
// with any other advisory lock taken against the same database. Any tool that
// mutates memberships directly in the database must take the lock under this
// same class, or it races the guarded mutations below.
const MemberLockClass = 0x4F52434D // "ORCM"

// MemberMutationRepository implements the guarded membership mutations.
type MemberMutationRepository struct {
	db *gorm.DB
}

// NewMemberMutationRepository wires the guarded membership mutation repository.
func NewMemberMutationRepository(db *gorm.DB) *MemberMutationRepository {
	if db == nil {
		panic("MemberMutationRepository requires a database handle")
	}
	return &MemberMutationRepository{db: db}
}

// lockAccount takes the account's advisory lock for the rest of the
// transaction. It blocks until the lock is free; it is released on commit or
// rollback, so no explicit unlock is needed.
func lockAccount(tx *gorm.DB, accountID uuid.UUID) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?, hashtext(?))", MemberLockClass, accountID.String()).Error
}

// countActiveSuperAdmins re-reads the invariant's input inside the caller's
// transaction, where the advisory lock is held.
func countActiveSuperAdmins(tx *gorm.DB, accountID uuid.UUID) (int64, error) {
	var count int64
	err := tx.Model(&RoleBindingModel{}).
		Joins(`JOIN account_members am ON am.account_id = role_bindings.account_id AND am.user_id = role_bindings.user_id`).
		Where(`role_bindings.account_id = ? AND role_bindings.role = 'super_admin' AND role_bindings.ou_id IS NULL AND am.status = 'active'`, accountID).
		Count(&count).Error
	return count, err
}

// holdsAccountWideSuperAdmin reports whether the member currently holds the
// binding the invariant protects.
func holdsAccountWideSuperAdmin(tx *gorm.DB, accountID, userID uuid.UUID) (bool, error) {
	var count int64
	err := tx.Model(&RoleBindingModel{}).
		Where(`account_id = ? AND user_id = ? AND role = 'super_admin' AND ou_id IS NULL`, accountID, userID).
		Count(&count).Error
	return count > 0, err
}

// ensureNotLastSuperAdmin fails when removing the target's super_admin status
// would leave the account without an active one. Only fires when the target
// actually holds that binding today.
func ensureNotLastSuperAdmin(tx *gorm.DB, accountID, userID uuid.UUID) error {
	isSuperAdmin, err := holdsAccountWideSuperAdmin(tx, accountID, userID)
	if err != nil {
		return err
	}
	if !isSuperAdmin {
		return nil
	}
	count, err := countActiveSuperAdmins(tx, accountID)
	if err != nil {
		return err
	}
	if count <= 1 {
		return app_errors.ErrLastSuperAdmin
	}
	return nil
}

// isActiveSuperAdmin reports whether the member is active AND holds an
// account-wide super_admin binding right now — the right to manage members and
// to issue invitations.
func isActiveSuperAdmin(tx *gorm.DB, accountID, userID uuid.UUID) (bool, error) {
	var count int64
	err := tx.Model(&RoleBindingModel{}).
		Joins(`JOIN account_members am ON am.account_id = role_bindings.account_id AND am.user_id = role_bindings.user_id`).
		Where(`role_bindings.account_id = ? AND role_bindings.user_id = ? AND role_bindings.role = 'super_admin' AND role_bindings.ou_id IS NULL AND am.status = 'active'`, accountID, userID).
		Count(&count).Error
	return count > 0, err
}

// revokeInvitesIssuedBy deletes the pending invitations a member issued. Called
// in the transaction that takes away the member's right to have issued them,
// so no invitation outlives that right: acceptance re-checks the issuer too,
// and the two together leave no window.
func revokeInvitesIssuedBy(tx *gorm.DB, accountID, userID uuid.UUID) error {
	return tx.Where("account_id = ? AND invited_by = ? AND accepted_at IS NULL", accountID, userID).
		Delete(&AccountInviteModel{}).Error
}

// sameIDSet reports whether two id lists hold the same set.
func sameIDSet(a, b []uuid.UUID) bool {
	seen := make(map[uuid.UUID]bool, len(a))
	for _, id := range a {
		seen[id] = true
	}
	other := make(map[uuid.UUID]bool, len(b))
	for _, id := range b {
		if !seen[id] {
			return false
		}
		other[id] = true
	}
	return len(seen) == len(other)
}

func (r *MemberMutationRepository) ReplaceBindingsGuarded(ctx context.Context, accountID uuid.UUID, req BindingsReplace) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccount(tx, accountID); err != nil {
			return err
		}
		// The writer's right is asked again under the lock: checked only before
		// it, a super_admin demoted or removed in between could still write,
		// and grant a role they no longer hold.
		ok, err := isActiveSuperAdmin(tx, accountID, req.ActorID)
		if err != nil {
			return err
		}
		if !ok {
			return app_errors.ErrUnauthorized
		}
		if req.ExpectedBindingIDs != nil {
			var current []uuid.UUID
			if err := tx.Model(&RoleBindingModel{}).
				Where("account_id = ? AND user_id = ?", accountID, req.UserID).
				Pluck("id", &current).Error; err != nil {
				return err
			}
			if !sameIDSet(current, *req.ExpectedBindingIDs) {
				return app_errors.ErrMemberBindingsStale
			}
		}
		if !req.KeepsSuperAdmin {
			if err := ensureNotLastSuperAdmin(tx, accountID, req.UserID); err != nil {
				return err
			}
			// Below super_admin nobody may invite, so nothing they issued stays.
			if err := revokeInvitesIssuedBy(tx, accountID, req.UserID); err != nil {
				return err
			}
		}
		if err := tx.Where("account_id = ? AND user_id = ?", accountID, req.UserID).
			Delete(&RoleBindingModel{}).Error; err != nil {
			return err
		}
		for i := range req.Bindings {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
				Create(roleBindingFromDomain(&req.Bindings[i])).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *MemberMutationRepository) SetStatusGuarded(ctx context.Context, accountID, userID uuid.UUID, status account_member.MemberStatus) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccount(tx, accountID); err != nil {
			return err
		}
		if status == account_member.StatusDisabled {
			if err := ensureNotLastSuperAdmin(tx, accountID, userID); err != nil {
				return err
			}
		}
		result := tx.Model(&AccountMemberModel{}).
			Where("account_id = ? AND user_id = ?", accountID, userID).
			Updates(map[string]interface{}{"status": string(status), "updated_at": time.Now()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrAccountMemberNotFound
		}
		if status == account_member.StatusDisabled {
			return revokeInvitesIssuedBy(tx, accountID, userID)
		}
		return nil
	})
}

func (r *MemberMutationRepository) RemoveGuarded(ctx context.Context, accountID, userID uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockAccount(tx, accountID); err != nil {
			return err
		}
		if err := ensureNotLastSuperAdmin(tx, accountID, userID); err != nil {
			return err
		}
		if err := tx.Where("account_id = ? AND user_id = ?", accountID, userID).
			Delete(&RoleBindingModel{}).Error; err != nil {
			return err
		}
		result := tx.Where("account_id = ? AND user_id = ?", accountID, userID).
			Delete(&AccountMemberModel{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return app_errors.ErrAccountMemberNotFound
		}
		return revokeInvitesIssuedBy(tx, accountID, userID)
	})
}
