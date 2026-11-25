package sql

import (
	"context"
	"errors"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Verify interface implementation
var _ repository.SSHKeyRepository = (*SSHKeyRepository)(nil)

// SSHKeyRepository is the implementation of the SSH key repository using SQL
type SSHKeyRepository struct {
	db        *gorm.DB
	encryptor Encryptor
}

// autoMigrate creates the necessary database tables
func (r *SSHKeyRepository) autoMigrate() error {
	return r.db.AutoMigrate(&SSHKeyModel{})
}

// Setup initializes the repository and creates tables if needed
func (r *SSHKeyRepository) Setup() error {
	return r.autoMigrate()
}

// NewSSHKeyRepository creates a new SSH key repository. The encryptor protects
// server-generated connection keypairs' private keys at rest (same key as
// secrets); it is required.
func NewSSHKeyRepository(db *gorm.DB, encryptor Encryptor) *SSHKeyRepository {
	if encryptor == nil {
		panic("SSHKeyRepository requires an encryptor")
	}
	return &SSHKeyRepository{
		db:        db,
		encryptor: encryptor,
	}
}

// Create creates a new SSH key
func (r *SSHKeyRepository) Create(ctx context.Context, key *ssh_key.SSHKey) error {
	model := &SSHKeyModel{}
	model.FromDomain(key)

	// Encrypt the private key at rest for server-generated connection keypairs.
	if key.PrivateKey != "" {
		enc, err := r.encryptor.Encrypt(key.PrivateKey)
		if err != nil {
			return err
		}
		model.PrivateKeyEncrypted = enc
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// Update updates an existing SSH key. The encrypted private key column is only
// touched when a new plaintext private key is supplied; otherwise it is left
// intact so an ordinary alias/tags edit cannot wipe a connection key's secret.
func (r *SSHKeyRepository) Update(ctx context.Context, key *ssh_key.SSHKey) error {
	model := &SSHKeyModel{}
	model.FromDomain(key)

	// Update the updatedAt timestamp
	model.UpdatedAt = time.Now()

	query := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", key.ID, key.AccountID)

	if key.PrivateKey != "" {
		enc, err := r.encryptor.Encrypt(key.PrivateKey)
		if err != nil {
			return err
		}
		model.PrivateKeyEncrypted = enc
	} else {
		// Preserve the stored ciphertext (FromDomain zeroed it).
		query = query.Omit("private_key_encrypted")
	}

	result := updateAllColumns(query, model)
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrSSHKeyNotFound
	}

	return nil
}

// GetWithPrivateKey loads a key and decrypts its private key (empty for
// public-only keys). For internal use only (outbound SSH auth) — never surface
// the returned PrivateKey through the API.
func (r *SSHKeyRepository) GetWithPrivateKey(ctx context.Context, accountID, keyID uuid.UUID) (*ssh_key.SSHKey, error) {
	var model SSHKeyModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", keyID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrSSHKeyNotFound
		}
		return nil, result.Error
	}

	domainKey := model.ToDomain()
	if model.PrivateKeyEncrypted != "" {
		plaintext, err := r.encryptor.Decrypt(model.PrivateKeyEncrypted)
		if err != nil {
			return nil, err
		}
		domainKey.PrivateKey = plaintext
	}
	return domainKey, nil
}

// GetByID retrieves an SSH key by ID within an account
func (r *SSHKeyRepository) GetByID(ctx context.Context, accountID, keyID uuid.UUID) (*ssh_key.SSHKey, error) {
	var model SSHKeyModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", keyID, accountID).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrSSHKeyNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(), nil
}

// GetByIDs batch-loads keys by id within an account. Missing ids are omitted.
func (r *SSHKeyRepository) GetByIDs(ctx context.Context, accountID uuid.UUID, keyIDs []uuid.UUID) ([]ssh_key.SSHKey, error) {
	if len(keyIDs) == 0 {
		return nil, nil
	}
	var models []SSHKeyModel
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND id IN ?", accountID, keyIDs).
		Limit(listRunawayCeiling).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}
	keys := make([]ssh_key.SSHKey, len(models))
	for i, model := range models {
		keys[i] = *model.ToDomain()
	}
	return keys, nil
}

// GetAllByAccountID retrieves all SSH keys for a specific account
func (r *SSHKeyRepository) GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]ssh_key.SSHKey, error) {
	var models []SSHKeyModel
	result := r.db.WithContext(ctx).
		Where("account_id = ?", accountID).
		Order("created_at DESC").
		Limit(listRunawayCeiling).
		Find(&models)
	if result.Error != nil {
		return nil, result.Error
	}

	// Convert models to domain objects
	keys := make([]ssh_key.SSHKey, len(models))
	for i, model := range models {
		domainKey := model.ToDomain()
		keys[i] = *domainKey
	}

	return keys, nil
}

// Delete deletes an SSH key by ID within an account
func (r *SSHKeyRepository) Delete(ctx context.Context, accountID, keyID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Where("id = ? AND account_id = ?", keyID, accountID).
		Delete(&SSHKeyModel{})
	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return app_errors.ErrSSHKeyNotFound
	}

	return nil
}

// ClearProvisioningReferences nulls every provisioning ssh_key_id pointing at
// this key and returns the per-table row counts.
//
// Null-and-report, not cascade-delete and not restrict. Deleting a key is a
// REVOCATION — often of a key that must stop working right now — so refusing
// the delete because a node still references it would leave an operator with a
// live compromised key and a list of rows to hand-edit. Nulling degrades the
// affected level to whatever it inherits (node -> OU -> root -> account
// default), which is the same shape the policy resolver already implements.
//
// One transaction: a partial clear would leave exactly the dangling reference
// this exists to prevent.
func (r *SSHKeyRepository) ClearProvisioningReferences(ctx context.Context, accountID, keyID uuid.UUID) (map[string]int64, error) {
	cleared := map[string]int64{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		targets := []struct {
			name  string
			model interface{}
		}{
			{"nodes", &NodeModel{}},
			{"organizational_units", &OrganizationalUnitModel{}},
			{"account_policies", &AccountPolicyModel{}},
			{"master_tokens", &MasterTokenModel{}},
		}
		for _, target := range targets {
			result := tx.Model(target.model).
				Where("account_id = ? AND ssh_key_id = ?", accountID, keyID).
				Update("ssh_key_id", nil)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				cleared[target.name] = result.RowsAffected
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cleared, nil
}

// UnsetDefaultForAccount unsets IsDefault flag for all keys in an account
func (r *SSHKeyRepository) UnsetDefaultForAccount(ctx context.Context, accountID uuid.UUID) error {
	result := r.db.WithContext(ctx).
		Model(&SSHKeyModel{}).
		Where("account_id = ? AND is_default = ?", accountID, true).
		Update("is_default", false)
	if result.Error != nil {
		return result.Error
	}

	return nil
}

// GetDefaultByAccountID retrieves the default SSH key for an account
func (r *SSHKeyRepository) GetDefaultByAccountID(ctx context.Context, accountID uuid.UUID) (*ssh_key.SSHKey, error) {
	var model SSHKeyModel
	result := r.db.WithContext(ctx).
		Where("account_id = ? AND is_default = ?", accountID, true).
		First(&model)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, app_errors.ErrSSHKeyNotFound
		}
		return nil, result.Error
	}

	return model.ToDomain(), nil
}
