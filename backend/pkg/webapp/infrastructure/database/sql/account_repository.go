package sql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"gorm.io/gorm"
)

// Verify interface implementation
var _ repository.AccountRepository = (*AccountRepository)(nil)
var _ repository.AccountDeviceLimitReader = (*AccountRepository)(nil)

// AccountRepository is the implementation of the account repository using SQL
type AccountRepository struct {
	db *gorm.DB
}

// autoMigrate creates the necessary database tables
func (r *AccountRepository) autoMigrate() error {
	return r.db.AutoMigrate(&AccountModel{})
}

// Setup initializes the repository and creates tables if needed
func (r *AccountRepository) Setup() error {
	return r.autoMigrate()
}

// NewAccountRepository creates a new repository for account operations
func NewAccountRepository(db *gorm.DB) *AccountRepository {
	repo := &AccountRepository{
		db: db,
	}
	return repo
}

// Create creates a new account
func (r *AccountRepository) Create(ctx context.Context, account *account.Account) error {
	model := &AccountModel{}
	model.FromDomain(account)

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// GetByID retrieves an account by its ID
func (r *AccountRepository) GetByID(ctx context.Context, id uuid.UUID) (*account.Account, error) {
	var model AccountModel
	result := r.db.WithContext(ctx).Where("id = ?", id).First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrAccountNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(), nil
}

// accountAuditRetentionColumn is written by instance administration only.
const accountAuditRetentionColumn = "audit_retention_days"

// accountOpenSignupPendingColumn is written only when the account is created
// and when it settles (SettleOpenSignup).
const accountOpenSignupPendingColumn = "open_signup_pending_since"

// accountDeviceLimitColumns are the per-account device-limit override,
// written by instance administration only.
var accountDeviceLimitColumns = []string{"device_limit", "device_limit_until"}

// Update updates an existing account. It never writes the audit retention
// column: that value is set outside the tenant API, and a full-row write from
// a copy read before it changed would put the old value back. Nor the
// pending-sign-up mark, for the same reason: a copy read before the account
// settled would mark it pending again. Nor the device-limit override, set
// outside the tenant API like the retention.
func (r *AccountRepository) Update(ctx context.Context, account *account.Account) error {
	model := &AccountModel{}
	model.FromDomain(account)

	result := updateAllColumns(r.db.WithContext(ctx).Where("id = ?", account.ID).Omit(append([]string{accountAuditRetentionColumn, accountOpenSignupPendingColumn}, accountDeviceLimitColumns...)...), model)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrAccountNotFound
	}

	return nil
}

// Counts returns the admission counts on this instance (see the port).
func (r *AccountRepository) Counts(ctx context.Context, pendingSince time.Time) (repository.AccountCounts, error) {
	return countAccounts(r.db.WithContext(ctx), pendingSince)
}

// countAccounts reads the three admission counts in one statement, so they
// describe the same instant.
func countAccounts(db *gorm.DB, pendingSince time.Time) (repository.AccountCounts, error) {
	var c struct {
		Total   int64
		Settled int64
		Pending int64
	}
	err := db.Model(&AccountModel{}).Select(
		"COUNT(*) AS total, "+
			"COUNT(*) FILTER (WHERE "+accountOpenSignupPendingColumn+" IS NULL) AS settled, "+
			"COUNT(*) FILTER (WHERE "+accountOpenSignupPendingColumn+" >= ?) AS pending", pendingSince).
		Scan(&c).Error
	return repository.AccountCounts{Total: c.Total, Settled: c.Settled, Pending: c.Pending}, err
}

// InstanceOperatorAccountID is the account that operates this install: the
// oldest one, by created_at with the id as the tiebreak, so the answer is
// one row and the same row on every read. An install with no account
// answers app_errors.ErrAccountNotFound.
func (r *AccountRepository) InstanceOperatorAccountID(ctx context.Context) (uuid.UUID, error) {
	var m AccountModel
	err := r.db.WithContext(ctx).Model(&AccountModel{}).Select("id").
		Order("created_at ASC").Order("id ASC").First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return uuid.Nil, app_errors.ErrAccountNotFound
	}
	return m.ID, err
}

// AccountCeilingLockClass is the constant key of the advisory lock that
// serializes self-registration admissions. Its own constant, distinct from
// the device cap's (DeviceCapLockClass) and the member mutations': sharing
// one would make a burst of device admissions queue every sign-up behind
// them, and the two counts have nothing to do with each other.
const AccountCeilingLockClass = 0x4F524143 // "ORAC"

// CreateAdmitted counts, decides and inserts in one transaction under the
// constant advisory lock — see the port for the rule. The lock is
// transaction-scoped, so a refused admission releases it with the rollback
// and a committed insert with the commit; the next waiter then counts the
// row that was just inserted.
func (r *AccountRepository) CreateAdmitted(ctx context.Context, account *account.Account, pendingSince time.Time, admit repository.AccountAdmission) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		counts, err := lockAndCount(tx, pendingSince)
		if err != nil {
			return err
		}
		if err := admit(counts, account); err != nil {
			return err
		}
		// Built AFTER the admission: the admission may mark the candidate as a
		// pending open sign-up, and the row must carry what it decided.
		model := &AccountModel{}
		model.FromDomain(account)
		return tx.Create(model).Error
	})
}

// lockAndCount takes the admission lock and reads the counts inside the
// caller's transaction.
func lockAndCount(tx *gorm.DB, pendingSince time.Time) (repository.AccountCounts, error) {
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", AccountCeilingLockClass).Error; err != nil {
		return repository.AccountCounts{}, fmt.Errorf("%w: %v", repository.ErrAccountCountUnavailable, err)
	}
	counts, err := countAccounts(tx, pendingSince)
	if err != nil {
		// Fail CLOSED: nothing is written and the caller refuses. The count
		// decides whether an invite code is required at all, so an unknown
		// count must not become "the first owner".
		return repository.AccountCounts{}, fmt.Errorf("%w: %v", repository.ErrAccountCountUnavailable, err)
	}
	return counts, nil
}

// SettleOpenSignup clears the pending mark under the admission lock when the
// admission accepts (see the port). The UPDATE is conditional on the mark
// still being set, so two settles racing for one account both end settled
// and the second changes nothing.
func (r *AccountRepository) SettleOpenSignup(ctx context.Context, id uuid.UUID, pendingSince time.Time, admit repository.AccountAdmission) (bool, error) {
	settled := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		counts, err := lockAndCount(tx, pendingSince)
		if err != nil {
			return err
		}
		var m AccountModel
		if err := tx.Where("id = ?", id).First(&m).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return app_errors.ErrAccountNotFound
			}
			return err
		}
		if m.OpenSignupPendingSince == nil {
			settled = true
			return nil
		}
		if err := admit(counts, m.ToDomain()); err != nil {
			return err
		}
		if err := tx.Model(&AccountModel{}).
			Where("id = ? AND "+accountOpenSignupPendingColumn+" IS NOT NULL", id).
			Updates(map[string]interface{}{accountOpenSignupPendingColumn: nil, "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		settled = true
		return nil
	})
	return settled, err
}

// Delete deletes an account by its UUID
func (r *AccountRepository) Delete(ctx context.Context, id uuid.UUID) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&AccountModel{})
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrAccountNotFound
	}

	return nil
}

// DeviceLimitOverride reads the account's per-account device-limit override
// (see the port). An account that does not exist answers
// app_errors.ErrAccountNotFound.
func (r *AccountRepository) DeviceLimitOverride(ctx context.Context, id uuid.UUID) (entitlement.AccountLimitOverride, error) {
	var row struct {
		DeviceLimit      *int
		DeviceLimitUntil *time.Time
	}
	err := r.db.WithContext(ctx).Model(&AccountModel{}).Select(accountDeviceLimitColumns).
		Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return entitlement.AccountLimitOverride{}, app_errors.ErrAccountNotFound
	}
	if err != nil {
		return entitlement.AccountLimitOverride{}, err
	}
	return entitlement.AccountLimitOverride{Limit: row.DeviceLimit, Until: row.DeviceLimitUntil}, nil
}
