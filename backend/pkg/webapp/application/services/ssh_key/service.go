package ssh_key

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_generate"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/ssh_key_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/ssh_key_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/ssh_key_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/tags"
	"github.com/google/uuid"
)

// Service handles SSH key-related business logic
// AccessPropagator pushes an access apply to a node fleet after a key change.
// Implemented by the access service; wired optionally to avoid a hard
// dependency.
type AccessPropagator interface {
	PropagateKeyChange(ctx context.Context, accountID uuid.UUID)
}

type Service struct {
	sshKeyRepo      repository.SSHKeyRepository
	accessEntryRepo repository.AccessEntryRepository
	propagator      AccessPropagator
	audit           *audit.Recorder
	logger          *slog.Logger
}

// NewService creates a new SSH key service
func NewService(sshKeyRepo repository.SSHKeyRepository, auditRecorder *audit.Recorder, logger *slog.Logger) *Service {
	if auditRecorder == nil {
		panic("ssh_key.Service requires an audit recorder")
	}
	return &Service{
		sshKeyRepo: sshKeyRepo,
		audit:      auditRecorder,
		logger:     logger,
	}
}

// SetAccessEntryRepo wires the access-entry repository so deleting a key also
// removes it from every access entry that referenced it (optional; when unset
// the cascade is skipped). Emptying a user entry's key set locks that user
// everywhere the entry applies — the "revoke this key fleet-wide" path.
func (s *Service) SetAccessEntryRepo(repo repository.AccessEntryRepository) {
	s.accessEntryRepo = repo
}

// SetAccessPropagator wires the access-apply dispatcher so deleting a key also
// pushes the resulting access change (users that lost the key) to the fleet.
func (s *Service) SetAccessPropagator(p AccessPropagator) {
	s.propagator = p
}

// Create creates a new SSH key for the authenticated account
func (s *Service) Create(ctx context.Context, cmd ssh_key_create.SSHKeyCreateCommand) (*ssh_key.SSHKey, error) {
	// Determine key type (default to SSH if not specified)
	keyType := ssh_key.KeyType(cmd.KeyType)
	if keyType == "" {
		keyType = ssh_key.KeyTypeSSH
	}

	// Create new SSH key
	newKey, err := ssh_key.NewSSHKey(cmd.AuthContext.AccountID, cmd.Alias, cmd.KeyContent, keyType)
	if err != nil {
		// The domain's format and type checks word their refusal for the
		// operator; it is the request that must change.
		return nil, app_errors.InvalidRequest("invalid_ssh_key", err)
	}
	newKey.ID = cmd.ID
	normalized, err := tags.Normalize(cmd.Tags)
	if err != nil {
		return nil, app_errors.InvalidRequest("invalid_tag", err)
	}
	newKey.Tags = normalized

	// Check if this is the first key for the account
	existingKeys, err := s.sshKeyRepo.GetAllByAccountID(ctx, cmd.AuthContext.AccountID)
	if err != nil {
		return nil, err
	}

	// If it's the first key, set it as default
	if len(existingKeys) == 0 {
		newKey.IsDefault = true
	}

	// Save to repository
	err = s.sshKeyRepo.Create(ctx, newKey)
	if err != nil {
		return nil, err
	}

	// The alias and key TYPE are recorded, never the key content. A public key is
	// not a secret, but a private one shares the column family and the habit of
	// "log the key" is how the wrong one ends up in a table nobody encrypts.
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionSSHKeyCreate, audit_event.TargetSSHKey, newKey.ID.String(),
		fmt.Sprintf("alias=%q type=%s default=%t", newKey.Alias, newKey.KeyType, newKey.IsDefault))

	return newKey, nil
}

// GenerateConnectionKey generates a new ed25519 connection keypair: the public
// part is stored (and deployed to nodes) like any SSH key; the private part is
// encrypted at rest and used by the platform for outbound SSH authentication.
// The returned key carries the plaintext PrivateKey (freshly generated) but the
// caller MUST NOT surface it — only the public part is downloadable.
func (s *Service) GenerateConnectionKey(ctx context.Context, cmd ssh_key_generate.SSHKeyGenerateCommand) (*ssh_key.SSHKey, error) {
	alias := strings.TrimSpace(cmd.Alias)
	if alias == "" {
		return nil, app_errors.InvalidRequest("invalid_alias", fmt.Errorf("alias is required"))
	}

	pub, priv, err := generateEd25519Keypair("orchestrator-" + alias)
	if err != nil {
		return nil, err
	}

	newKey, err := ssh_key.NewSSHKey(cmd.AuthContext.AccountID, alias, pub, ssh_key.KeyTypeSSH)
	if err != nil {
		return nil, err
	}
	newKey.ID = cmd.ID
	newKey.PrivateKey = priv
	newKey.HasPrivateKey = true

	normalized, err := tags.Normalize(cmd.Tags)
	if err != nil {
		return nil, app_errors.InvalidRequest("invalid_tag", err)
	}
	newKey.Tags = normalized

	existingKeys, err := s.sshKeyRepo.GetAllByAccountID(ctx, cmd.AuthContext.AccountID)
	if err != nil {
		return nil, err
	}
	if len(existingKeys) == 0 {
		newKey.IsDefault = true
	}

	if err := s.sshKeyRepo.Create(ctx, newKey); err != nil {
		return nil, err
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionSSHKeyGenerate, audit_event.TargetSSHKey, newKey.ID.String(),
		fmt.Sprintf("alias=%q ed25519 connection keypair", newKey.Alias))
	return newKey, nil
}

// GetPublicKey returns the public key text (authorized_keys line) of a key. This
// is the only key material the API exposes for download.
func (s *Service) GetPublicKey(ctx context.Context, accountID, keyID uuid.UUID) (string, error) {
	key, err := s.sshKeyRepo.GetByID(ctx, accountID, keyID)
	if err != nil {
		return "", err
	}
	return key.KeyContent, nil
}

// GetPrivateKey returns the decrypted private key of a connection keypair. For
// INTERNAL use only (outbound SSH auth in the SSH-adopt flow) — never expose it
// through an API response. Returns an empty string for public-only keys.
func (s *Service) GetPrivateKey(ctx context.Context, accountID, keyID uuid.UUID) (string, error) {
	key, err := s.sshKeyRepo.GetWithPrivateKey(ctx, accountID, keyID)
	if err != nil {
		return "", err
	}
	return key.PrivateKey, nil
}

// Update updates an existing SSH key if it belongs to the account
func (s *Service) Update(ctx context.Context, cmd ssh_key_update.SSHKeyUpdateCommand) (*ssh_key.SSHKey, error) {
	existingKey, err := s.sshKeyRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}

	// Update alias if provided
	if cmd.Alias != nil {
		existingKey.Alias = *cmd.Alias
	}

	// Update key content if provided. Not for a generated connection keypair:
	// its stored private key is what SSH-adopt authenticates with, so a new
	// public half would no longer match it and every adoption would fail.
	if cmd.KeyContent != nil {
		if existingKey.HasPrivateKey {
			return nil, app_errors.InvalidRequest("ssh_key_has_private_key",
				errors.New("this is a generated connection key: its public key cannot be replaced; generate a new key instead"))
		}
		if err := existingKey.UpdateKeyContent(*cmd.KeyContent); err != nil {
			return nil, app_errors.InvalidRequest("invalid_ssh_key", err)
		}
	}

	// Handle default key logic
	if cmd.IsDefault != nil && *cmd.IsDefault {
		// Unset default for all other keys in the account
		if err := s.sshKeyRepo.UnsetDefaultForAccount(ctx, cmd.AuthContext.AccountID); err != nil {
			return nil, err
		}
		existingKey.IsDefault = true
	} else if cmd.IsDefault != nil && !*cmd.IsDefault {
		// Allow unsetting default
		existingKey.IsDefault = false
	}

	if cmd.Tags != nil {
		normalized, err := tags.Normalize(*cmd.Tags)
		if err != nil {
			return nil, app_errors.InvalidRequest("invalid_tag", err)
		}
		existingKey.Tags = normalized
	}

	existingKey.UpdatedAt = time.Now()

	err = s.sshKeyRepo.Update(ctx, existingKey)
	if err != nil {
		return nil, err
	}

	// Replacing key_content re-points every access entry that references this key
	// at different key material without touching a single entry, which is why an
	// update is recorded as loudly as a create.
	changed := make([]string, 0, 3)
	if cmd.Alias != nil {
		changed = append(changed, fmt.Sprintf("alias=%q", *cmd.Alias))
	}
	if cmd.KeyContent != nil {
		changed = append(changed, "key content replaced")
	}
	if cmd.IsDefault != nil {
		changed = append(changed, fmt.Sprintf("default=%t", *cmd.IsDefault))
	}
	if cmd.Tags != nil {
		changed = append(changed, fmt.Sprintf("tags=%d", len(existingKey.Tags)))
	}
	if len(changed) == 0 {
		changed = append(changed, "no field changed")
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionSSHKeyUpdate, audit_event.TargetSSHKey, existingKey.ID.String(),
		strings.Join(changed, ", "))

	// Replacing the key MATERIAL is a fleet-wide revocation of the old material,
	// exactly like deleting the key — so it has to reach the devices the same
	// way. Without this the database says the old public key is gone while
	// `/etc/ssh/authorized_keys.d/%u` on every node still authorises it, and
	// nothing corrects that until the node happens to RECONNECT (the applied
	// generation self-heal runs from UpdateSystemInfo, which fires on connect,
	// not on a timer). A node that stays up for a month keeps honouring a key
	// the operator believes they rotated away.
	//
	// Only on a content change: an alias edit, a default flip or a tag change
	// alters nothing the device is holding, and pushing an apply to the whole
	// account for them would make renaming a key a fleet event.
	if cmd.KeyContent != nil && s.propagator != nil {
		s.propagator.PropagateKeyChange(ctx, cmd.AuthContext.AccountID)
	}

	return existingKey, nil
}

// GetByID retrieves an SSH key by ID if it belongs to the account
func (s *Service) GetByID(ctx context.Context, query ssh_key_get.SSHKeyGetQuery) (*ssh_key.SSHKey, error) {
	key, err := s.sshKeyRepo.GetByID(ctx, query.AuthContext.AccountID, query.ID)
	if err != nil {
		return nil, err
	}

	return key, nil
}

// GetAllByAccountID lists all SSH keys for the account
func (s *Service) GetAllByAccountID(ctx context.Context, query ssh_key_list.SSHKeyListQuery) ([]ssh_key.SSHKey, error) {
	keys, err := s.sshKeyRepo.GetAllByAccountID(ctx, query.AuthContext.AccountID)
	if err != nil {
		return nil, err
	}

	return keys, nil
}

// GetDefaultByAccountID retrieves the default SSH key for an account
func (s *Service) GetDefaultByAccountID(ctx context.Context, accountID uuid.UUID) (*ssh_key.SSHKey, error) {
	key, err := s.sshKeyRepo.GetDefaultByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}

	return key, nil
}

// Delete deletes an SSH key if it belongs to the account
func (s *Service) Delete(ctx context.Context, accountID, actorUserID, keyID uuid.UUID) error {
	// Get key before deletion to check if it was default
	keyToDelete, err := s.sshKeyRepo.GetByID(ctx, accountID, keyID)
	if err != nil {
		return err
	}

	wasDefault := keyToDelete.IsDefault

	// Cascade into access entries FIRST, and hard-fail on error: dropping the
	// join rows before the key row guarantees the safe direction — there is
	// never a moment where an entry references a key that no longer exists,
	// which would otherwise freeze every subsequent access-config resolve on the
	// key-count check. A user entry left with no keys becomes locked (derived).
	if s.accessEntryRepo != nil {
		if err := s.accessEntryRepo.DeleteKeyReferences(ctx, accountID, keyID); err != nil {
			return fmt.Errorf("failed to remove key from access entries: %w", err)
		}
	}

	// Same reasoning, second holder of this id: the four provisioning
	// ssh_key_id columns (nodes, OUs, the account policy, master tokens). A
	// dangling one used to make ResolveEffectiveProvisioning return an error,
	// which is not a local failure — it is every install_os fill-in, every
	// playbook resolve and every apply_access_config for that node, failing
	// until someone edits the database. Clearing degrades the level to what it
	// inherits instead.
	clearedRefs, err := s.sshKeyRepo.ClearProvisioningReferences(ctx, accountID, keyID)
	if err != nil {
		return fmt.Errorf("failed to clear provisioning references to key: %w", err)
	}

	if err := s.sshKeyRepo.Delete(ctx, accountID, keyID); err != nil {
		return err
	}

	if len(clearedRefs) > 0 {
		// Loud on purpose: an OU that silently stops handing out a provisioning
		// key looks like nothing happened until the next install comes up
		// without it.
		s.logger.WarnContext(ctx, "deleted SSH key was still referenced for provisioning; those references were cleared",
			"ssh_key_id", keyID, "account_id", accountID, "cleared", clearedRefs)
	}

	// Deleting a key is a fleet-wide revocation: the cascade above stripped it
	// from every access entry, and an entry left with no keys becomes a LOCKED OS
	// user. "Everyone lost their login and no entry was edited" needs an author.
	s.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionSSHKeyDelete, audit_event.TargetSSHKey, keyID.String(),
		fmt.Sprintf("alias=%q type=%s was_default=%t cleared_provisioning_refs=%v",
			keyToDelete.Alias, keyToDelete.KeyType, wasDefault, clearedRefs))

	// Push the resulting access change (users that just lost this key, possibly
	// locking them) to the fleet, so a key removed from the inventory is actually
	// revoked on the devices and not just in the database.
	if s.propagator != nil {
		s.propagator.PropagateKeyChange(ctx, accountID)
	}

	// If the deleted key was default, promote the oldest remaining key so the
	// account keeps a default when one exists.
	if wasDefault {
		remainingKeys, err := s.sshKeyRepo.GetAllByAccountID(ctx, accountID)
		if err == nil && len(remainingKeys) > 0 {
			// Set the oldest key (first in created_at order) as default
			oldestKey := remainingKeys[0]
			oldestKey.IsDefault = true
			if err := s.sshKeyRepo.UnsetDefaultForAccount(ctx, accountID); err == nil {
				_ = s.sshKeyRepo.Update(ctx, &oldestKey)
			}
		}
	}

	return nil
}
