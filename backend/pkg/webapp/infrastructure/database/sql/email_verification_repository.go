package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/email_verification"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ repository.EmailVerificationRepository = (*EmailVerificationRepository)(nil)

// EmailVerificationModel is the GORM model for pending mailbox proofs.
type EmailVerificationModel struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	UserID    uuid.UUID  `gorm:"column:user_id;type:uuid;not null;index"`
	Email     string     `gorm:"column:email;size:255;not null"`
	TokenHash string     `gorm:"column:token_hash;size:64;not null;uniqueIndex"`
	ExpiresAt time.Time  `gorm:"column:expires_at;not null;index"`
	UsedAt    *time.Time `gorm:"column:used_at"`
	CreatedAt time.Time  `gorm:"column:created_at;not null"`
}

func (EmailVerificationModel) TableName() string {
	return "email_verifications"
}

func (m *EmailVerificationModel) ToDomain() *email_verification.EmailVerification {
	return &email_verification.EmailVerification{
		ID:        m.ID,
		UserID:    m.UserID,
		Email:     m.Email,
		TokenHash: m.TokenHash,
		ExpiresAt: m.ExpiresAt,
		UsedAt:    m.UsedAt,
		CreatedAt: m.CreatedAt,
	}
}

func emailVerificationFromDomain(v *email_verification.EmailVerification) *EmailVerificationModel {
	return &EmailVerificationModel{
		ID:        v.ID,
		UserID:    v.UserID,
		Email:     v.Email,
		TokenHash: v.TokenHash,
		ExpiresAt: v.ExpiresAt,
		UsedAt:    v.UsedAt,
		CreatedAt: v.CreatedAt,
	}
}

// EmailVerificationRepository is the GORM implementation.
type EmailVerificationRepository struct {
	db *gorm.DB
}

func NewEmailVerificationRepository(db *gorm.DB) *EmailVerificationRepository {
	if db == nil {
		panic("EmailVerificationRepository requires a database handle")
	}
	return &EmailVerificationRepository{db: db}
}

func (r *EmailVerificationRepository) Setup() error {
	return r.db.AutoMigrate(&EmailVerificationModel{})
}

func (r *EmailVerificationRepository) Create(ctx context.Context, verification *email_verification.EmailVerification) error {
	return r.db.WithContext(ctx).Create(emailVerificationFromDomain(verification)).Error
}

// ClaimByHash consumes the token in a single conditional UPDATE: the TTL and
// single-use checks live in the WHERE clause, so two concurrent clicks cannot
// both land (exactly one gets RowsAffected == 1).
func (r *EmailVerificationRepository) ClaimByHash(ctx context.Context, tokenHash string) (*email_verification.EmailVerification, error) {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&EmailVerificationModel{}).
		Where("token_hash = ? AND used_at IS NULL AND expires_at > ?", tokenHash, now).
		Update("used_at", now)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, app_errors.ErrEmailVerificationInvalid
	}
	var model EmailVerificationModel
	if err := r.db.WithContext(ctx).
		Where("token_hash = ?", tokenHash).
		First(&model).Error; err != nil {
		return nil, err
	}
	return model.ToDomain(), nil
}

// RevokeUnusedByUserID spends a user's outstanding links instead of deleting
// them. Spending is what ClaimByHash checks, so the link is just as dead — and
// the row survives for CountRecentByUserID, which is the resend throttle's only
// evidence that the mail went out.
func (r *EmailVerificationRepository) RevokeUnusedByUserID(ctx context.Context, userID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&EmailVerificationModel{}).
		Where("user_id = ? AND used_at IS NULL", userID).
		Update("used_at", time.Now()).Error
}

func (r *EmailVerificationRepository) CountRecentByUserID(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&EmailVerificationModel{}).
		Where("user_id = ? AND created_at > ?", userID, since).
		Count(&count).Error
	return count, err
}

// DeleteExpired prunes rows that can no longer be claimed. Spent rows are kept
// for a grace period so a double-click on the link can still be told apart from
// a forged token in the logs.
func (r *EmailVerificationRepository) DeleteExpired(ctx context.Context) (int64, error) {
	cutoff := time.Now().Add(-24 * time.Hour)
	result := r.db.WithContext(ctx).
		Where("expires_at < ? OR used_at < ?", cutoff, cutoff).
		Delete(&EmailVerificationModel{})
	return result.RowsAffected, result.Error
}
