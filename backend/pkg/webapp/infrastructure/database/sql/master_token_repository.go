package sql

import (
	"context"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ repository.MasterTokenRepository = (*MasterTokenRepository)(nil)

type MasterTokenRepository struct {
	db *gorm.DB
	// enc protects master_tokens.default_password at rest. It is not a dormant
	// field: the token's password is copied onto every node adopted with it and
	// becomes that node's provisioning credential.
	enc Encryptor
}

func NewMasterTokenRepository(db *gorm.DB, enc Encryptor) *MasterTokenRepository {
	if enc == nil {
		panic("MasterTokenRepository requires an encryptor")
	}
	return &MasterTokenRepository{db: db, enc: enc}
}

func (r *MasterTokenRepository) Setup() error {
	return r.db.AutoMigrate(&MasterTokenModel{})
}

// BackfillDefaultPasswordEncryption encrypts the token passwords stored before
// the marker column existed. Non-fatal by contract.
func (r *MasterTokenRepository) BackfillDefaultPasswordEncryption() (int, error) {
	return backfillProvisioningPasswords(r.db, r.enc, "master_tokens", "id")
}

// toDomain converts a row and decrypts its password. Every read path goes
// through it.
func (r *MasterTokenRepository) toDomain(m *MasterTokenModel) *master_token.MasterToken {
	t := m.ToDomain()
	t.DefaultPassword, t.DefaultPasswordUnreadable = decryptProvisioningPassword(
		r.enc, m.DefaultPassword, m.DefaultPasswordEnc, "master_tokens."+m.ID.String())
	return t
}

// fromDomain fills a row and encrypts its password, reporting the columns to
// omit from the write when an undecryptable value must be left alone.
func (r *MasterTokenRepository) fromDomain(m *MasterTokenModel, t *master_token.MasterToken) ([]string, error) {
	m.FromDomain(t)
	if t.DefaultPasswordUnreadable && t.DefaultPassword == "" {
		return []string{"default_password", "default_password_enc"}, nil
	}
	stored, marker, err := encryptProvisioningPassword(r.enc, t.DefaultPassword)
	if err != nil {
		return nil, err
	}
	m.DefaultPassword = stored
	m.DefaultPasswordEnc = marker
	return nil, nil
}

func (r *MasterTokenRepository) Create(ctx context.Context, token *master_token.MasterToken) error {
	model := &MasterTokenModel{}
	if _, err := r.fromDomain(model, token); err != nil {
		return err
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

func (r *MasterTokenRepository) Update(ctx context.Context, token *master_token.MasterToken) error {
	model := &MasterTokenModel{}
	omitPassword, err := r.fromDomain(model, token)
	if err != nil {
		return err
	}
	model.UpdatedAt = time.Now()

	// Every column (not a partial Updates) so cleared pointer fields (e.g. ou_id)
	// are written.
	result := updateAllColumns(r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", token.ID, token.AccountID).
		Omit(omitPassword...), model)

	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrMasterTokenNotFound
	}

	return nil
}

func (r *MasterTokenRepository) GetByID(ctx context.Context, accountID, tokenID uuid.UUID) (*master_token.MasterToken, error) {
	var model MasterTokenModel

	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", tokenID, accountID).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, app_errors.ErrMasterTokenNotFound
		}
		return nil, result.Error
	}

	return r.toDomain(&model), nil
}

func (r *MasterTokenRepository) GetByTokenHash(ctx context.Context, tokenHash string) (*master_token.MasterToken, error) {
	var model MasterTokenModel

	result := r.db.WithContext(ctx).
		Where("token_hash = ?", tokenHash).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, app_errors.ErrMasterTokenNotFound
		}
		return nil, result.Error
	}

	return r.toDomain(&model), nil
}

func (r *MasterTokenRepository) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]master_token.MasterToken, error) {
	var models []MasterTokenModel

	result := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Limit(listRunawayCeiling).
		Find(&models)

	if result.Error != nil {
		return nil, result.Error
	}

	tokens := make([]master_token.MasterToken, len(models))
	for i, model := range models {
		tokens[i] = *r.toDomain(&model)
	}

	return tokens, nil
}

func (r *MasterTokenRepository) Delete(ctx context.Context, accountID, tokenID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", tokenID, accountID).
		Delete(&MasterTokenModel{})

	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrMasterTokenNotFound
	}

	return nil
}

// DeleteByOUID revokes every token scoped to an OU (OU deletion cleanup).
func (r *MasterTokenRepository) DeleteByOUID(ctx context.Context, accountID, ouID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Where("account_id = ? AND ou_id = ?", accountID, ouID).
		Delete(&MasterTokenModel{}).Error
}

func (r *MasterTokenRepository) UpdateLastUsed(ctx context.Context, tokenID uuid.UUID) error {
	now := time.Now()

	result := r.db.WithContext(ctx).
		Model(&MasterTokenModel{}).
		Where("id = ?", tokenID).
		Updates(map[string]interface{}{
			"last_used_at": now,
			"updated_at":   now,
		})

	if result.Error != nil {
		return result.Error
	}

	return nil
}
