package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/passkey"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ repository.PasskeyRepository = (*PasskeyRepository)(nil)

// PasskeyModel is the GORM model for a registered WebAuthn credential.
//
// Nothing here is secret: a credential is a public key plus a handle. It is
// still scoped and indexed carefully, because the credential id is the lookup
// key for usernameless login and must resolve to exactly one owner.
type PasskeyModel struct {
	ID             uuid.UUID  `gorm:"column:id;type:uuid;primaryKey"`
	UserID         uuid.UUID  `gorm:"column:user_id;type:uuid;not null;index"`
	CredentialID   []byte     `gorm:"column:credential_id;type:bytea;not null;uniqueIndex"`
	PublicKey      []byte     `gorm:"column:public_key;type:bytea;not null"`
	AAGUID         []byte     `gorm:"column:aaguid;type:bytea"`
	SignCount      uint32     `gorm:"column:sign_count;not null;default:0"`
	BackupEligible bool       `gorm:"column:backup_eligible;not null;default:false"`
	BackupState    bool       `gorm:"column:backup_state;not null;default:false"`
	Name           string     `gorm:"column:name;size:120"`
	CreatedAt      time.Time  `gorm:"column:created_at;not null"`
	LastUsedAt     *time.Time `gorm:"column:last_used_at"`
}

func (PasskeyModel) TableName() string {
	return "passkeys"
}

func (m *PasskeyModel) ToDomain() *passkey.Passkey {
	return &passkey.Passkey{
		ID:             m.ID,
		UserID:         m.UserID,
		CredentialID:   m.CredentialID,
		PublicKey:      m.PublicKey,
		AAGUID:         m.AAGUID,
		SignCount:      m.SignCount,
		BackupEligible: m.BackupEligible,
		BackupState:    m.BackupState,
		Name:           m.Name,
		CreatedAt:      m.CreatedAt,
		LastUsedAt:     m.LastUsedAt,
	}
}

func passkeyFromDomain(p *passkey.Passkey) *PasskeyModel {
	return &PasskeyModel{
		ID:             p.ID,
		UserID:         p.UserID,
		CredentialID:   p.CredentialID,
		PublicKey:      p.PublicKey,
		AAGUID:         p.AAGUID,
		SignCount:      p.SignCount,
		BackupEligible: p.BackupEligible,
		BackupState:    p.BackupState,
		Name:           p.Name,
		CreatedAt:      p.CreatedAt,
		LastUsedAt:     p.LastUsedAt,
	}
}

// PasskeyRepository is the GORM implementation.
type PasskeyRepository struct {
	db *gorm.DB
}

func NewPasskeyRepository(db *gorm.DB) *PasskeyRepository {
	if db == nil {
		panic("PasskeyRepository requires a database handle")
	}
	return &PasskeyRepository{db: db}
}

func (r *PasskeyRepository) Setup() error {
	return r.db.AutoMigrate(&PasskeyModel{})
}

func (r *PasskeyRepository) Create(ctx context.Context, p *passkey.Passkey) error {
	return r.db.WithContext(ctx).Create(passkeyFromDomain(p)).Error
}

// CreateWhileSessionLive: see the port. A passkey is a complete way to sign
// in, so it is stored under the same boundary as an OIDC identity
// (lockLiveSession, user_repository.go) and not one line looser.
func (r *PasskeyRepository) CreateWhileSessionLive(ctx context.Context, p *passkey.Passkey, familyID uuid.UUID, issuedAt time.Time, generation int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockLiveSession(tx, p.UserID, familyID, issuedAt, generation); err != nil {
			return err
		}
		return tx.Create(passkeyFromDomain(p)).Error
	})
}

func (r *PasskeyRepository) ListByUserID(ctx context.Context, userID uuid.UUID) ([]passkey.Passkey, error) {
	var models []PasskeyModel
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at ASC").
		Find(&models).Error; err != nil {
		return nil, err
	}
	out := make([]passkey.Passkey, 0, len(models))
	for i := range models {
		out = append(out, *models[i].ToDomain())
	}
	return out, nil
}

func (r *PasskeyRepository) GetByCredentialID(ctx context.Context, credentialID []byte) (*passkey.Passkey, error) {
	var model PasskeyModel
	err := r.db.WithContext(ctx).Where("credential_id = ?", credentialID).First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrPasskeyNotFound
		}
		return nil, err
	}
	return model.ToDomain(), nil
}

func (r *PasskeyRepository) Update(ctx context.Context, p *passkey.Passkey) error {
	return r.db.WithContext(ctx).
		Model(&PasskeyModel{}).
		Where("id = ?", p.ID).
		Updates(map[string]interface{}{
			"sign_count":   p.SignCount,
			"last_used_at": p.LastUsedAt,
			"backup_state": p.BackupState,
			"name":         p.Name,
		}).Error
}

func (r *PasskeyRepository) Delete(ctx context.Context, userID, passkeyID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", passkeyID, userID).
		Delete(&PasskeyModel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return app_errors.ErrPasskeyNotFound
	}
	return nil
}

func (r *PasskeyRepository) CountByUserID(ctx context.Context, userID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&PasskeyModel{}).
		Where("user_id = ?", userID).
		Count(&count).Error
	return count, err
}
