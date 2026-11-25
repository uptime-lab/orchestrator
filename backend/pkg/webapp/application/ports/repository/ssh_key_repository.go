package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/google/uuid"
)

// SSHKeyRepository defines the interface for SSH key-related database operations
type SSHKeyRepository interface {
	// Create creates a new SSH key
	Create(ctx context.Context, key *ssh_key.SSHKey) error

	// Update updates an existing SSH key
	Update(ctx context.Context, key *ssh_key.SSHKey) error

	// GetByID retrieves an SSH key by ID within an account
	GetByID(ctx context.Context, accountID, keyID uuid.UUID) (*ssh_key.SSHKey, error)

	// GetByIDs batch-loads keys by id within an account (order unspecified).
	// Missing ids are simply absent from the result — callers detect them by
	// comparing lengths. Used to hydrate access-entry key sets without N+1.
	GetByIDs(ctx context.Context, accountID uuid.UUID, keyIDs []uuid.UUID) ([]ssh_key.SSHKey, error)

	// GetWithPrivateKey retrieves a key with its decrypted private key populated
	// (empty for public-only keys). For internal outbound-SSH use only — never
	// expose the returned PrivateKey through the API.
	GetWithPrivateKey(ctx context.Context, accountID, keyID uuid.UUID) (*ssh_key.SSHKey, error)

	// GetAllByAccountID retrieves all SSH keys for a specific account
	GetAllByAccountID(ctx context.Context, accountID uuid.UUID) ([]ssh_key.SSHKey, error)

	// Delete deletes an SSH key by ID within an account
	Delete(ctx context.Context, accountID, keyID uuid.UUID) error

	// ClearProvisioningReferences nulls every provisioning ssh_key_id that
	// points at this key — nodes, organizational units, the account policy and
	// master tokens — and reports how many rows it cleared per table.
	//
	// It lives on this repository rather than on four others because it is the
	// referential integrity of THIS id: a dangling reference is not a property
	// of the node table, it is what deleting a key does to everything holding
	// it. Call it BEFORE Delete, so there is never a moment where a row points
	// at a key that is already gone.
	ClearProvisioningReferences(ctx context.Context, accountID, keyID uuid.UUID) (map[string]int64, error)

	// UnsetDefaultForAccount unsets IsDefault flag for all keys in an account
	UnsetDefaultForAccount(ctx context.Context, accountID uuid.UUID) error

	// GetDefaultByAccountID retrieves the default SSH key for an account
	GetDefaultByAccountID(ctx context.Context, accountID uuid.UUID) (*ssh_key.SSHKey, error)
}
