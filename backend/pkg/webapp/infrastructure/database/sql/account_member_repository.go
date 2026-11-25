package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_member"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.AccountMemberRepository = (*AccountMemberRepository)(nil)

// AccountMemberModel is the GORM model for account memberships.
type AccountMemberModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid;not null;uniqueIndex:uq_account_members_account_user;index"`
	UserID    uuid.UUID `gorm:"column:user_id;type:uuid;not null;uniqueIndex:uq_account_members_account_user;index"`
	Status    string    `gorm:"column:status;not null;default:active"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

func (AccountMemberModel) TableName() string {
	return "account_members"
}

func accountMemberFromDomain(m *account_member.AccountMember) *AccountMemberModel {
	return &AccountMemberModel{
		ID:        m.ID,
		AccountID: m.AccountID,
		UserID:    m.UserID,
		Status:    string(m.Status),
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

func (m *AccountMemberModel) ToDomain() *account_member.AccountMember {
	return &account_member.AccountMember{
		ID:        m.ID,
		AccountID: m.AccountID,
		UserID:    m.UserID,
		Status:    account_member.MemberStatus(m.Status),
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// AccountMemberRepository is the GORM implementation of the account member
// repository.
type AccountMemberRepository struct {
	db *gorm.DB
}

func NewAccountMemberRepository(db *gorm.DB) *AccountMemberRepository {
	return &AccountMemberRepository{db: db}
}

func (r *AccountMemberRepository) Setup() error {
	return r.db.AutoMigrate(&AccountMemberModel{})
}

func (r *AccountMemberRepository) Create(ctx context.Context, member *account_member.AccountMember) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_id"}, {Name: "user_id"}},
			DoNothing: true,
		}).
		Create(accountMemberFromDomain(member)).Error
}

func (r *AccountMemberRepository) GetByAccountAndUser(ctx context.Context, accountID, userID uuid.UUID) (*account_member.AccountMember, error) {
	var model AccountMemberModel
	err := r.db.WithContext(ctx).
		Where("account_id = ? AND user_id = ?", accountID, userID).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrAccountMemberNotFound
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

// CountByAccountID counts memberships of every status — see the port comment
// for why a suspended member still occupies a seat.
func (r *AccountMemberRepository) CountByAccountID(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&AccountMemberModel{}).
		Where("account_id = ?", accountID).
		Count(&n).Error
	return n, err
}

func (r *AccountMemberRepository) ListByAccountID(ctx context.Context, accountID uuid.UUID) ([]account_member.AccountMember, error) {
	var models []AccountMemberModel
	err := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at ASC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	members := make([]account_member.AccountMember, 0, len(models))
	for i := range models {
		members = append(members, *models[i].ToDomain())
	}
	return members, nil
}

func (r *AccountMemberRepository) ListByUserID(ctx context.Context, userID uuid.UUID) ([]account_member.AccountMember, error) {
	var models []AccountMemberModel
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at ASC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	members := make([]account_member.AccountMember, 0, len(models))
	for i := range models {
		members = append(members, *models[i].ToDomain())
	}
	return members, nil
}

func (r *AccountMemberRepository) UpdateStatus(ctx context.Context, accountID, userID uuid.UUID, status account_member.MemberStatus) error {
	result := r.db.WithContext(ctx).
		Model(&AccountMemberModel{}).
		Where("account_id = ? AND user_id = ?", accountID, userID).
		Updates(map[string]interface{}{"status": string(status), "updated_at": time.Now()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrAccountMemberNotFound
	}
	return nil
}

func (r *AccountMemberRepository) Delete(ctx context.Context, accountID, userID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND user_id = ?", accountID, userID).
		Delete(&AccountMemberModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrAccountMemberNotFound
	}
	return nil
}

func (r *AccountMemberRepository) DeleteByAccountID(ctx context.Context, accountID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Delete(&AccountMemberModel{}).Error
}
