package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.AccountRoleSettingRepository = (*AccountRoleSettingRepository)(nil)

// AccountRoleSettingModel is the GORM model for role-permission overrides.
type AccountRoleSettingModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid;not null;uniqueIndex:uq_account_role_settings;index"`
	Role      string    `gorm:"column:role;not null;uniqueIndex:uq_account_role_settings"`
	Action    string    `gorm:"column:action;not null;uniqueIndex:uq_account_role_settings"`
	Allowed   bool      `gorm:"column:allowed;not null"`
	UpdatedBy uuid.UUID `gorm:"column:updated_by;type:uuid;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

func (AccountRoleSettingModel) TableName() string {
	return "account_role_settings"
}

func accountRoleSettingFromDomain(s *account_role_setting.AccountRoleSetting) *AccountRoleSettingModel {
	return &AccountRoleSettingModel{
		ID:        s.ID,
		AccountID: s.AccountID,
		Role:      string(s.Role),
		Action:    s.Action,
		Allowed:   s.Allowed,
		UpdatedBy: s.UpdatedBy,
		UpdatedAt: s.UpdatedAt,
	}
}

func (m *AccountRoleSettingModel) ToDomain() *account_role_setting.AccountRoleSetting {
	return &account_role_setting.AccountRoleSetting{
		ID:        m.ID,
		AccountID: m.AccountID,
		Role:      authz.Role(m.Role),
		Action:    m.Action,
		Allowed:   m.Allowed,
		UpdatedBy: m.UpdatedBy,
		UpdatedAt: m.UpdatedAt,
	}
}

// AccountRoleSettingRepository is the GORM implementation of the role-setting
// repository.
type AccountRoleSettingRepository struct {
	db *gorm.DB
}

func NewAccountRoleSettingRepository(db *gorm.DB) *AccountRoleSettingRepository {
	return &AccountRoleSettingRepository{db: db}
}

func (r *AccountRoleSettingRepository) Setup() error {
	return r.db.AutoMigrate(&AccountRoleSettingModel{})
}

func (r *AccountRoleSettingRepository) Upsert(ctx context.Context, setting *account_role_setting.AccountRoleSetting) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_id"}, {Name: "role"}, {Name: "action"}},
			DoUpdates: clause.AssignmentColumns([]string{"allowed", "updated_by", "updated_at"}),
		}).
		Create(accountRoleSettingFromDomain(setting)).Error
}

func (r *AccountRoleSettingRepository) ListByAccountID(ctx context.Context, accountID uuid.UUID) ([]account_role_setting.AccountRoleSetting, error) {
	var models []AccountRoleSettingModel
	err := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("role ASC, action ASC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	settings := make([]account_role_setting.AccountRoleSetting, 0, len(models))
	for i := range models {
		settings = append(settings, *models[i].ToDomain())
	}
	return settings, nil
}

func (r *AccountRoleSettingRepository) Delete(ctx context.Context, accountID uuid.UUID, role, action string) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND role = ? AND action = ?", accountID, role, action).
		Delete(&AccountRoleSettingModel{}).Error
}

func (r *AccountRoleSettingRepository) DeleteByAccountID(ctx context.Context, accountID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Delete(&AccountRoleSettingModel{}).Error
}
