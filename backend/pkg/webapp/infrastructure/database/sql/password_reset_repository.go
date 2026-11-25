package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/password_reset"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ repository.PasswordResetRepository = (*PasswordResetRepository)(nil)

// PasswordResetModel is the GORM model for pending reset grants.
type PasswordResetModel struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid;not null;index"`
	Email     string     `gorm:"column:email;size:255;not null"`
	TokenHash string     `gorm:"column:token_hash;size:64;not null;uniqueIndex"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null;index"`
	UsedAt    *time.Time `gorm:"column:used_at"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
}

func (PasswordResetModel) TableName() string {
	return "password_resets"
}

func (m *PasswordResetModel) ToDomain() *password_reset.PasswordReset {
	return &password_reset.PasswordReset{
		ID:        m.ID,
		UserID:    m.UserID,
		Email:     m.Email,
		TokenHash: m.TokenHash,
		ExpiresAt: m.ExpiresAt,
		UsedAt:    m.UsedAt,
		CreatedAt: m.CreatedAt,
	}
}

func passwordResetFromDomain(r *password_reset.PasswordReset) *PasswordResetModel {
	return &PasswordResetModel{
		ID:        r.ID,
		UserID:    r.UserID,
		Email:     r.Email,
		TokenHash: r.TokenHash,
		ExpiresAt: r.ExpiresAt,
		UsedAt:    r.UsedAt,
		CreatedAt: r.CreatedAt,
	}
}

// PasswordResetRepository is the GORM implementation.
type PasswordResetRepository struct {
	db *gorm.DB
}

func NewPasswordResetRepository(db *gorm.DB) *PasswordResetRepository {
	if db == nil {
		panic("PasswordResetRepository requires a database handle")
	}
	return &PasswordResetRepository{db: db}
}

func (r *PasswordResetRepository) Setup() error {
	return r.db.AutoMigrate(&PasswordResetModel{})
}

func (r *PasswordResetRepository) Create(ctx context.Context, reset *password_reset.PasswordReset) error {
	return r.db.WithContext(ctx).Create(passwordResetFromDomain(reset)).Error
}

// ClaimByHash consumes the token in a single conditional UPDATE: the TTL and
// single-use checks live in the WHERE clause, so two concurrent submissions
// cannot both land (exactly one gets RowsAffected == 1).
func (r *PasswordResetRepository) ClaimByHash(ctx context.Context, tokenHash string) (*password_reset.PasswordReset, error) {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&PasswordResetModel{}).
		Where("token_hash = ? AND used_at IS NULL AND expires_at > ?", tokenHash, now).
		Update("used_at", now)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, app_errors.ErrPasswordResetInvalid
	}
	var model PasswordResetModel
	if err := r.db.WithContext(ctx).
		Where("token_hash = ?", tokenHash).
		First(&model).Error; err != nil {
		return nil, err
	}
	return model.ToDomain(), nil
}

// RevokeUnusedByUserID spends a user's outstanding grants instead of deleting
// them. Spending is what ClaimByHash checks, so the link is just as dead — and
// the row survives for CountRecentByUserID, which is the send throttle's only
// evidence that the mail went out.
func (r *PasswordResetRepository) RevokeUnusedByUserID(ctx context.Context, userID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&PasswordResetModel{}).
		Where("user_id = ? AND used_at IS NULL", userID).
		Update("used_at", time.Now()).Error
}

func (r *PasswordResetRepository) CountRecentByUserID(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&PasswordResetModel{}).
		Where("user_id = ? AND created_at > ?", userID, since).
		Count(&count).Error
	return count, err
}

// DeleteExpired prunes rows that can no longer be claimed. Spent rows are kept
// for a grace period so a reused link can still be told apart from a forged
// token when reading the logs after an incident.
func (r *PasswordResetRepository) DeleteExpired(ctx context.Context) error {
	cutoff := time.Now().Add(-24 * time.Hour)
	return r.db.WithContext(ctx).
		Where("expires_at < ? OR used_at < ?", cutoff, cutoff).
		Delete(&PasswordResetModel{}).Error
}
