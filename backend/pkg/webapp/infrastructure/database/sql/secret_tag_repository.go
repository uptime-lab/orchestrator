package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret_tag"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ repository.SecretTagRepository = (*SecretTagRepository)(nil)

// SecretTagModel is the GORM row for a tag color override. Membership stays on
// secrets.tags; this row only carries the color + lifecycle.
type SecretTagModel struct {
	ID        uuid.UUID `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID uuid.UUID `gorm:"column:account_id;type:uuid;index;uniqueIndex:uq_secret_tags_account_name;not null"`
	Name      string    `gorm:"column:name;size:64;uniqueIndex:uq_secret_tags_account_name;not null"`
	Color     string    `gorm:"column:color;size:32;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (SecretTagModel) TableName() string {
	return "secret_tags"
}

func (m *SecretTagModel) toDomain() secret_tag.Tag {
	return secret_tag.Tag{
		AccountID: m.AccountID,
		Name:      m.Name,
		Color:     m.Color,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// SecretTagRepository stores tag color overrides and cascades rename/delete.
type SecretTagRepository struct {
	db *gorm.DB
}

func NewSecretTagRepository(db *gorm.DB) *SecretTagRepository {
	return &SecretTagRepository{db: db}
}

func (r *SecretTagRepository) Setup() error {
	return r.db.AutoMigrate(&SecretTagModel{})
}

func (r *SecretTagRepository) List(ctx context.Context, accountID uuid.UUID) ([]secret_tag.Tag, error) {
	var models []SecretTagModel
	if err := r.db.WithContext(ctx).Where("account_id = ?", accountID).Order("name asc").Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]secret_tag.Tag, len(models))
	for i := range models {
		out[i] = models[i].toDomain()
	}
	return out, nil
}

func (r *SecretTagRepository) GetByName(ctx context.Context, accountID uuid.UUID, name string) (*secret_tag.Tag, error) {
	var model SecretTagModel
	err := r.db.WithContext(ctx).Where("account_id = ? AND name = ?", accountID, name).First(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, app_errors.ErrSecretTagNotFound
	}
	if err != nil {
		return nil, err
	}
	d := model.toDomain()
	return &d, nil
}

// Upsert inserts the tag row or updates its color, keyed by (account_id, name).
func (r *SecretTagRepository) Upsert(ctx context.Context, tag *secret_tag.Tag) error {
	model := SecretTagModel{
		ID:        uuid.New(),
		AccountID: tag.AccountID,
		Name:      tag.Name,
		Color:     tag.Color,
		CreatedAt: tag.CreatedAt,
		UpdatedAt: tag.UpdatedAt,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "account_id"}, {Name: "name"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"color": tag.Color, "updated_at": time.Now()}),
	}).Create(&model).Error
}

// nameInUse reports whether a tag name is referenced anywhere in the account
// (a tag row, a secret's tags array, or a tag attachment).
func (r *SecretTagRepository) nameInUse(ctx context.Context, tx *gorm.DB, accountID uuid.UUID, name string) (bool, error) {
	var count int64
	if err := tx.WithContext(ctx).Model(&SecretTagModel{}).
		Where("account_id = ? AND name = ?", accountID, name).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if err := tx.WithContext(ctx).Model(&SecretModel{}).
		Where("account_id = ? AND ? = ANY(tags)", accountID, name).Count(&count).Error; err != nil {
		return false, err
	}
	if count > 0 {
		return true, nil
	}
	if err := tx.WithContext(ctx).Model(&SecretAttachmentModel{}).
		Where("account_id = ? AND tag = ?", accountID, name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Rename cascades a tag rename across secrets, attachments and the tag row.
func (r *SecretTagRepository) Rename(ctx context.Context, accountID uuid.UUID, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		inUse, err := r.nameInUse(ctx, tx, accountID, newName)
		if err != nil {
			return err
		}
		if inUse {
			return app_errors.ErrSecretTagNameTaken
		}
		// Membership: replace the string inside every carrying secret.
		if err := tx.Exec(
			`UPDATE secrets SET tags = array_replace(tags, ?, ?), updated_at = now()
			 WHERE account_id = ? AND ? = ANY(tags)`,
			oldName, newName, accountID, oldName,
		).Error; err != nil {
			return err
		}
		// Tag attachments carrying the old subject.
		if err := tx.Exec(
			`UPDATE secret_attachments SET tag = ? WHERE account_id = ? AND tag = ?`,
			newName, accountID, oldName,
		).Error; err != nil {
			return err
		}
		// The color row (if any).
		return tx.Exec(
			`UPDATE secret_tags SET name = ?, updated_at = now() WHERE account_id = ? AND name = ?`,
			newName, accountID, oldName,
		).Error
	})
}

// Delete cascades a tag removal across secrets, attachments and the tag row.
func (r *SecretTagRepository) Delete(ctx context.Context, accountID uuid.UUID, name string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`UPDATE secrets SET tags = array_remove(tags, ?), updated_at = now()
			 WHERE account_id = ? AND ? = ANY(tags)`,
			name, accountID, name,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(
			`DELETE FROM secret_attachments WHERE account_id = ? AND tag = ?`,
			accountID, name,
		).Error; err != nil {
			return err
		}
		return tx.Exec(
			`DELETE FROM secret_tags WHERE account_id = ? AND name = ?`,
			accountID, name,
		).Error
	})
}
