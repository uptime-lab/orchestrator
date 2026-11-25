package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/adoption_code"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Verify interface implementation
var _ repository.AdoptionCodeRepository = (*AdoptionCodeRepository)(nil)

// AdoptionCodeRepository is the SQL implementation of the adoption code repository
type AdoptionCodeRepository struct {
	db *gorm.DB
}

// NewAdoptionCodeRepository creates a new adoption code repository
func NewAdoptionCodeRepository(db *gorm.DB) *AdoptionCodeRepository {
	return &AdoptionCodeRepository{db: db}
}

// Setup initializes the repository and creates tables if needed
func (r *AdoptionCodeRepository) Setup() error {
	return r.db.AutoMigrate(&AdoptionCodeModel{})
}

// Create stores a freshly minted code.
func (r *AdoptionCodeRepository) Create(ctx context.Context, code *adoption_code.AdoptionCode) error {
	model := &AdoptionCodeModel{}
	model.FromDomain(code)
	return r.db.WithContext(ctx).Create(model).Error
}

// ClaimByHash atomically consumes an unused, unexpired code: the conditional
// UPDATE is the single-use guarantee — only one concurrent claimer sees
// RowsAffected == 1. Unknown, expired and already-used codes fail identically.
func (r *AdoptionCodeRepository) ClaimByHash(ctx context.Context, codeHash string) (*adoption_code.AdoptionCode, error) {
	now := time.Now()
	result := r.db.WithContext(ctx).
		Model(&AdoptionCodeModel{}).
		Where("code_hash = ? AND used_at IS NULL AND expires_at > ?", codeHash, now).
		Update("used_at", now)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, app_errors.ErrAdoptionCodeInvalid
	}

	var model AdoptionCodeModel
	if err := r.db.WithContext(ctx).Where("code_hash = ?", codeHash).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrAdoptionCodeInvalid
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

// DeleteByNodeID revokes all codes minted for a node.
func (r *AdoptionCodeRepository) DeleteByNodeID(ctx context.Context, nodeID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("node_id = ?", nodeID).
		Delete(&AdoptionCodeModel{}).Error
}

// DeleteExpired sweeps stale rows.
func (r *AdoptionCodeRepository) DeleteExpired(ctx context.Context) error {
	return r.db.WithContext(ctx).
		Where("expires_at < ?", time.Now()).
		Delete(&AdoptionCodeModel{}).Error
}
