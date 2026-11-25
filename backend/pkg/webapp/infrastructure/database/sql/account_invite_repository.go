package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_invite"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ repository.AccountInviteRepository = (*AccountInviteRepository)(nil)

// AccountInviteModel is the GORM model for account invites.
type AccountInviteModel struct {
	ID         uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	AccountID  uuid.UUID  `gorm:"column:account_id;type:uuid;not null;index"`
	Email      string     `gorm:"column:email;not null"`
	Role       string     `gorm:"column:role;not null"`
	OUID       *uuid.UUID `gorm:"column:ou_id;type:uuid"`
	TokenHash  string     `gorm:"column:token_hash;not null;uniqueIndex"`
	InvitedBy  uuid.UUID  `gorm:"column:invited_by;type:uuid;not null"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;not null"`
	AcceptedAt *time.Time `gorm:"column:accepted_at"`
	CreatedAt  time.Time  `gorm:"column:created_at;not null"`
}

func (AccountInviteModel) TableName() string {
	return "account_invites"
}

func accountInviteFromDomain(i *account_invite.AccountInvite) *AccountInviteModel {
	return &AccountInviteModel{
		ID:         i.ID,
		AccountID:  i.AccountID,
		Email:      i.Email,
		Role:       string(i.Role),
		OUID:       i.OUID,
		TokenHash:  i.TokenHash,
		InvitedBy:  i.InvitedBy,
		ExpiresAt:  i.ExpiresAt,
		AcceptedAt: i.AcceptedAt,
		CreatedAt:  i.CreatedAt,
	}
}

func (m *AccountInviteModel) ToDomain() *account_invite.AccountInvite {
	return &account_invite.AccountInvite{
		ID:         m.ID,
		AccountID:  m.AccountID,
		Email:      m.Email,
		Role:       authz.Role(m.Role),
		OUID:       m.OUID,
		TokenHash:  m.TokenHash,
		InvitedBy:  m.InvitedBy,
		ExpiresAt:  m.ExpiresAt,
		AcceptedAt: m.AcceptedAt,
		CreatedAt:  m.CreatedAt,
	}
}

// AccountInviteRepository is the GORM implementation of the account invite
// repository.
type AccountInviteRepository struct {
	db *gorm.DB
}

func NewAccountInviteRepository(db *gorm.DB) *AccountInviteRepository {
	return &AccountInviteRepository{db: db}
}

func (r *AccountInviteRepository) Setup() error {
	if err := r.db.AutoMigrate(&AccountInviteModel{}); err != nil {
		return err
	}
	// One pending invite per email per account (partial index — accepted
	// invites are kept as records and must not block a re-invite).
	return r.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_account_invites_pending_email
		ON account_invites (account_id, lower(email)) WHERE accepted_at IS NULL`).Error
}

func (r *AccountInviteRepository) Create(ctx context.Context, invite *account_invite.AccountInvite) error {
	return r.db.WithContext(ctx).Create(accountInviteFromDomain(invite)).Error
}

func (r *AccountInviteRepository) GetByTokenHash(ctx context.Context, tokenHash string) (*account_invite.AccountInvite, error) {
	var model AccountInviteModel
	err := r.db.WithContext(ctx).
		Where("token_hash = ?", tokenHash).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrAccountInviteNotFound
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

func (r *AccountInviteRepository) GetByID(ctx context.Context, accountID, inviteID uuid.UUID) (*account_invite.AccountInvite, error) {
	var model AccountInviteModel
	err := r.db.WithContext(ctx).
		Where("account_id = ? AND id = ?", accountID, inviteID).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrAccountInviteNotFound
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

func (r *AccountInviteRepository) GetPendingByEmail(ctx context.Context, email string) (*account_invite.AccountInvite, error) {
	var model AccountInviteModel
	err := r.db.WithContext(ctx).
		Where("lower(email) = ? AND accepted_at IS NULL AND expires_at > ?", account_invite.NormalizeEmail(email), time.Now()).
		Order("created_at ASC").
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrAccountInviteNotFound
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

func (r *AccountInviteRepository) ListPendingByAccountID(ctx context.Context, accountID uuid.UUID) ([]account_invite.AccountInvite, error) {
	var models []AccountInviteModel
	err := r.db.WithContext(ctx).
		Where("account_id = ? AND accepted_at IS NULL AND expires_at > ?", accountID, time.Now()).
		Order("created_at ASC").
		Find(&models).Error
	if err != nil {
		return nil, err
	}
	invites := make([]account_invite.AccountInvite, 0, len(models))
	for i := range models {
		invites = append(invites, *models[i].ToDomain())
	}
	return invites, nil
}

func (r *AccountInviteRepository) Accept(ctx context.Context, tokenHash string) (bool, error) {
	// Atomic single-use: only a pending, unexpired invite flips to accepted.
	result := r.db.WithContext(ctx).Exec(
		`UPDATE account_invites SET accepted_at = ? WHERE token_hash = ? AND accepted_at IS NULL AND expires_at > ?`,
		time.Now(), tokenHash, time.Now())
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (r *AccountInviteRepository) Delete(ctx context.Context, accountID, inviteID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND id = ? AND accepted_at IS NULL", accountID, inviteID).
		Delete(&AccountInviteModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrAccountInviteNotFound
	}
	return nil
}

func (r *AccountInviteRepository) DeletePendingByEmail(ctx context.Context, accountID uuid.UUID, email string) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND lower(email) = ? AND accepted_at IS NULL", accountID, account_invite.NormalizeEmail(email)).
		Delete(&AccountInviteModel{}).Error
}

func (r *AccountInviteRepository) DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND ou_id = ? AND accepted_at IS NULL", accountID, ouID).
		Delete(&AccountInviteModel{}).Error
}

func (r *AccountInviteRepository) DeleteByAccountID(ctx context.Context, accountID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Delete(&AccountInviteModel{}).Error
}
