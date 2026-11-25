package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/role_binding"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.RoleBindingRepository = (*RoleBindingRepository)(nil)

// RoleBindingModel is the GORM model for role bindings.
type RoleBindingModel struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	AccountID uuid.UUID  `gorm:"column:account_id;type:uuid;not null;index"`
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid;not null;index"`
	Role      string     `gorm:"column:role;not null"`
	OUID      *uuid.UUID `gorm:"column:ou_id;type:uuid;index"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
	CreatedBy *uuid.UUID `gorm:"column:created_by;type:uuid"`
}

func (RoleBindingModel) TableName() string {
	return "role_bindings"
}

func roleBindingFromDomain(b *role_binding.RoleBinding) *RoleBindingModel {
	return &RoleBindingModel{
		ID:        b.ID,
		AccountID: b.AccountID,
		UserID:    b.UserID,
		Role:      string(b.Role),
		OUID:      b.OUID,
		CreatedAt: b.CreatedAt,
		CreatedBy: b.CreatedBy,
	}
}

func (m *RoleBindingModel) ToDomain() *role_binding.RoleBinding {
	return &role_binding.RoleBinding{
		ID:        m.ID,
		AccountID: m.AccountID,
		UserID:    m.UserID,
		Role:      authz.Role(m.Role),
		OUID:      m.OUID,
		CreatedAt: m.CreatedAt,
		CreatedBy: m.CreatedBy,
	}
}

// RoleBindingRepository is the GORM implementation of the role binding
// repository.
type RoleBindingRepository struct {
	db *gorm.DB
}

func NewRoleBindingRepository(db *gorm.DB) *RoleBindingRepository {
	return &RoleBindingRepository{db: db}
}

// Setup creates the table, its partial unique indexes, and backfills
// memberships + super_admin bindings for legacy single-account users.
// It must run AFTER AccountMemberRepository.Setup() and UserRepository.Setup()
// (see cmd/server/main.go ordering).
func (r *RoleBindingRepository) Setup() error {
	if err := r.db.AutoMigrate(&RoleBindingModel{}); err != nil {
		return err
	}
	// Postgres treats NULLs as distinct in unique indexes, so account-wide
	// bindings (ou_id IS NULL) need their own partial index; GORM tags cannot
	// express the WHERE clause.
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_role_bindings_account_wide ON role_bindings (account_id, user_id, role) WHERE ou_id IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_role_bindings_ou ON role_bindings (account_id, user_id, role, ou_id) WHERE ou_id IS NOT NULL`,
	} {
		if err := r.db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return r.backfillLegacyUsers()
}

// backfillLegacyUsers grants every not-yet-migrated user (users.account_id
// still populated) an active membership and an account-wide super_admin
// binding in that account — legacy users were sole owners of their accounts.
//
// users.account_id is the consumable "un-migrated" marker: it is cleared by
// the auth rework in the same release that stops reading it. Until then the
// inserts are conflict-tolerant no-ops on restart, and once cleared this
// backfill can never resurrect a revoked membership or binding.
func (r *RoleBindingRepository) backfillLegacyUsers() error {
	// Fresh installs never had the legacy users.account_id column (it is absent
	// from UserModel), so the raw SELECT below would fail with 42703. Nothing to
	// backfill in that case. Mirrors the HasColumn guard in UserRepository.Setup.
	if !r.db.Migrator().HasColumn(&UserModel{}, "account_id") {
		return nil
	}
	var legacy []struct {
		ID        uuid.UUID
		AccountID uuid.UUID
	}
	// Raw SQL: the account_id column is deliberately absent from newer
	// UserModel versions, and it may reference an already-deleted account.
	err := r.db.Raw(`SELECT u.id, u.account_id FROM users u
		WHERE u.account_id IS NOT NULL
		AND EXISTS (SELECT 1 FROM accounts a WHERE a.id = u.account_id)`).Scan(&legacy).Error
	if err != nil {
		return err
	}
	for _, usr := range legacy {
		err := r.db.Transaction(func(tx *gorm.DB) error {
			now := time.Now()
			err := tx.Exec(`INSERT INTO account_members (id, account_id, user_id, status, created_at, updated_at)
				VALUES (?, ?, ?, 'active', ?, ?)
				ON CONFLICT (account_id, user_id) DO NOTHING`,
				uuid.New(), usr.AccountID, usr.ID, now, now).Error
			if err != nil {
				return err
			}
			err = tx.Exec(`INSERT INTO role_bindings (id, account_id, user_id, role, ou_id, created_at, created_by)
				VALUES (?, ?, ?, 'super_admin', NULL, ?, NULL)
				ON CONFLICT (account_id, user_id, role) WHERE ou_id IS NULL DO NOTHING`,
				uuid.New(), usr.AccountID, usr.ID, now).Error
			if err != nil {
				return err
			}
			// Consume the marker in the same transaction: a NULL account_id is
			// the sole "already migrated" predicate, so restarts become no-ops
			// and a later revocation of this membership/binding can never be
			// resurrected by re-running the backfill.
			return tx.Exec(`UPDATE users SET account_id = NULL WHERE id = ?`, usr.ID).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *RoleBindingRepository) Create(ctx context.Context, binding *role_binding.RoleBinding) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(roleBindingFromDomain(binding)).Error
}

func (r *RoleBindingRepository) ListByAccountAndUser(ctx context.Context, accountID, userID uuid.UUID) ([]role_binding.RoleBinding, error) {
	var models []RoleBindingModel
	err := r.db.WithContext(ctx).
		Where("account_id = ? AND user_id = ?", accountID, userID).
		Order("created_at ASC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	return roleBindingsToDomain(models), nil
}

func (r *RoleBindingRepository) ListByAccountID(ctx context.Context, accountID uuid.UUID) ([]role_binding.RoleBinding, error) {
	var models []RoleBindingModel
	err := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at ASC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	return roleBindingsToDomain(models), nil
}

func (r *RoleBindingRepository) ReplaceForUser(ctx context.Context, accountID, userID uuid.UUID, bindings []role_binding.RoleBinding) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Where("account_id = ? AND user_id = ?", accountID, userID).
			Delete(&RoleBindingModel{}).Error
		if err != nil {
			return err
		}
		for i := range bindings {
			err := tx.Clauses(clause.OnConflict{DoNothing: true}).
				Create(roleBindingFromDomain(&bindings[i])).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *RoleBindingRepository) DeleteByAccountAndUser(ctx context.Context, accountID, userID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND user_id = ?", accountID, userID).
		Delete(&RoleBindingModel{}).Error
}

func (r *RoleBindingRepository) DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND ou_id = ?", accountID, ouID).
		Delete(&RoleBindingModel{}).Error
}

func (r *RoleBindingRepository) DeleteByAccountID(ctx context.Context, accountID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Delete(&RoleBindingModel{}).Error
}

func roleBindingsToDomain(models []RoleBindingModel) []role_binding.RoleBinding {
	bindings := make([]role_binding.RoleBinding, 0, len(models))
	for i := range models {
		bindings = append(bindings, *models[i].ToDomain())
	}
	return bindings
}
