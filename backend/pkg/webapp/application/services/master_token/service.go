package master_token

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/master_token_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/master_token_get"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/queries/master_token_list"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/master_token"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/tags"
	"github.com/google/uuid"
)

// defaultMasterTokenTTL bounds tokens created without an explicit expiry
// (mirrors the SD-provisioning default).
const defaultMasterTokenTTL = 7 * 24 * time.Hour

type Service struct {
	masterTokenRepo repository.MasterTokenRepository
	sshKeyRepo      repository.SSHKeyRepository
	ouRepo          repository.OrganizationalUnitRepository
	audit           *audit.Recorder
	logger          *slog.Logger
}

func NewService(masterTokenRepo repository.MasterTokenRepository, sshKeyRepo repository.SSHKeyRepository, ouRepo repository.OrganizationalUnitRepository, auditRecorder *audit.Recorder, logger *slog.Logger) *Service {
	if auditRecorder == nil {
		panic("master_token.Service requires an audit recorder")
	}
	return &Service{
		masterTokenRepo: masterTokenRepo,
		sshKeyRepo:      sshKeyRepo,
		ouRepo:          ouRepo,
		audit:           auditRecorder,
		logger:          logger.With("service", "master_token"),
	}
}

// validateRefs refuses an SSH key or adoption-OU reference that does not
// resolve inside the caller's account. A master token is an account-wide
// adoption credential: a foreign OU id stored here would silently file every
// adopted device into a unit that does not exist for this tenant.
func (s *Service) validateRefs(ctx context.Context, accountID uuid.UUID, sshKeyID, ouID *uuid.UUID) error {
	if sshKeyID != nil && s.sshKeyRepo != nil {
		if _, err := s.sshKeyRepo.GetByID(ctx, accountID, *sshKeyID); err != nil {
			return fmt.Errorf("invalid SSH key ID: %w", err)
		}
	}
	if ouID != nil && s.ouRepo != nil {
		if _, err := s.ouRepo.GetByID(ctx, accountID, *ouID); err != nil {
			return fmt.Errorf("invalid organizational unit ID: %w", err)
		}
	}
	return nil
}

// Create creates a new master token
func (s *Service) Create(ctx context.Context, cmd master_token_create.MasterTokenCreateCommand) (*master_token.MasterToken, error) {
	if strings.TrimSpace(cmd.Name) == "" {
		return nil, app_errors.InvalidRequest("invalid_name", master_token.ErrInvalidName)
	}
	// A token minted already expired adopts nothing: every device presenting
	// it is refused, while the list shows it active. Refused here, where the
	// operator can still pick another date.
	if cmd.ExpiresAt != nil && !cmd.ExpiresAt.After(time.Now()) {
		return nil, app_errors.InvalidRequest("invalid_expires_at", errors.New("expires_at must be in the future"))
	}
	// Explicitly supplied references must resolve inside the account (the
	// default-key fallback below is already account-scoped by construction).
	if err := s.validateRefs(ctx, cmd.AuthContext.AccountID, cmd.SSHKeyID, cmd.OUID); err != nil {
		return nil, err
	}
	// If SSH key ID is not provided, try to get the default SSH key
	sshKeyID := cmd.SSHKeyID
	if sshKeyID == nil && s.sshKeyRepo != nil {
		defaultKey, err := s.sshKeyRepo.GetDefaultByAccountID(ctx, cmd.AuthContext.AccountID)
		if err == nil && defaultKey != nil {
			sshKeyID = &defaultKey.ID
		}
	}

	var token *master_token.MasterToken
	var err error

	// A master token is a device-adoption credential; an unbounded lifetime is
	// a standing risk (leaked one-liners keep working forever). Default to the
	// same 7-day expiry the SD-bundle path uses when the caller sets none —
	// callers wanting a longer window pass expires_at explicitly.
	expiresAt := cmd.ExpiresAt
	if expiresAt == nil {
		d := time.Now().Add(defaultMasterTokenTTL)
		expiresAt = &d
	}
	token, err = master_token.NewMasterTokenWithExpiry(cmd.AuthContext.AccountID, cmd.Name, cmd.DefaultUsername, cmd.DefaultPassword, sshKeyID, *expiresAt)

	if err != nil {
		s.logger.ErrorContext(ctx, "failed to create master token domain object", "error", err)
		return nil, err
	}

	token.OUID = cmd.OUID
	normalized, err := tags.Normalize(cmd.Tags)
	if err != nil {
		return nil, app_errors.InvalidRequest("invalid_tag", err)
	}
	token.Tags = normalized

	if err := s.masterTokenRepo.Create(ctx, token); err != nil {
		s.logger.ErrorContext(ctx, "failed to save master token", "error", err)
		return nil, err
	}

	s.logger.InfoContext(ctx, "master token created",
		"token_id", token.ID,
		"account_id", token.AccountID,
		"name", token.Name)

	// Minting a master token is a GRANT: any device holding it can join this
	// account. The name, expiry and adoption OU are recorded; the token value is
	// not — only its hash is even stored.
	detail := fmt.Sprintf("name=%q expires=%s", token.Name, expiresAt.UTC().Format(time.RFC3339))
	if token.OUID != nil {
		detail += " adoption ou " + token.OUID.String()
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionMasterTokenCreate, audit_event.TargetMasterToken, token.ID.String(), detail)

	return token, nil
}

// Update updates an existing master token
func (s *Service) Update(ctx context.Context, cmd master_token_update.MasterTokenUpdateCommand) (*master_token.MasterToken, error) {
	token, err := s.masterTokenRepo.GetByID(ctx, cmd.AuthContext.AccountID, cmd.ID)
	if err != nil {
		return nil, err
	}

	if cmd.Name != nil {
		token.Name = *cmd.Name
	}

	if cmd.IsActive != nil {
		if *cmd.IsActive {
			token.Activate()
		} else {
			token.Deactivate()
		}
	}

	if cmd.DefaultUsername != nil {
		token.DefaultUsername = *cmd.DefaultUsername
	}

	if cmd.DefaultPassword != nil {
		token.DefaultPassword = *cmd.DefaultPassword
		// Explicitly supplied, so the repository's preserve-unreadable guard
		// must stand down or an explicit clear no-ops.
		token.DefaultPasswordUnreadable = false
	}

	// SSHKeyID: nil = unchanged, "" = clear, UUID = set.
	if cmd.SSHKeyID != nil {
		if *cmd.SSHKeyID == "" {
			token.SSHKeyID = nil
		} else {
			keyID, err := uuid.Parse(*cmd.SSHKeyID)
			if err != nil {
				return nil, app_errors.ErrSSHKeyNotFound
			}
			token.SSHKeyID = &keyID
		}
	}

	// OUID: nil = unchanged, "" = clear (stop auto-assigning adopted devices),
	// UUID = set the adoption target OU.
	if cmd.OUID != nil {
		if *cmd.OUID == "" {
			token.OUID = nil
		} else {
			ouID, err := uuid.Parse(*cmd.OUID)
			if err != nil {
				return nil, app_errors.ErrOrganizationalUnitNotFound
			}
			token.OUID = &ouID
		}
	}

	if cmd.Tags != nil {
		normalized, err := tags.Normalize(*cmd.Tags)
		if err != nil {
			return nil, app_errors.InvalidRequest("invalid_tag", err)
		}
		token.Tags = normalized
	}

	if cmd.ExpiresAt != nil {
		token.ExpiresAt = cmd.ExpiresAt
	}

	// Only refs this request supplied; a clear ("" OU) leaves nothing to check.
	{
		var checkSSH, checkOU *uuid.UUID
		if cmd.SSHKeyID != nil {
			checkSSH = token.SSHKeyID
		}
		if cmd.OUID != nil {
			checkOU = token.OUID
		}
		if err := s.validateRefs(ctx, cmd.AuthContext.AccountID, checkSSH, checkOU); err != nil {
			return nil, err
		}
	}

	if err := s.masterTokenRepo.Update(ctx, token); err != nil {
		s.logger.ErrorContext(ctx, "failed to update master token", "error", err, "token_id", cmd.ID)
		return nil, err
	}

	s.logger.InfoContext(ctx, "master token updated", "token_id", token.ID)

	// is_active and expires_at are the two that matter: re-activating a token or
	// pushing out its expiry revives an adoption credential that had stopped
	// working, which is indistinguishable from minting a new one in effect.
	changed := make([]string, 0, 4)
	if cmd.Name != nil {
		changed = append(changed, fmt.Sprintf("name=%q", *cmd.Name))
	}
	if cmd.IsActive != nil {
		changed = append(changed, fmt.Sprintf("is_active=%t", *cmd.IsActive))
	}
	if cmd.ExpiresAt != nil {
		changed = append(changed, "expires="+cmd.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if cmd.OUID != nil {
		changed = append(changed, "adoption ou changed")
	}
	if cmd.DefaultPassword != nil {
		// Presence only: this one is encrypted at rest.
		changed = append(changed, "default password set")
	}
	if len(changed) == 0 {
		changed = append(changed, "no notable field changed")
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionMasterTokenUpdate, audit_event.TargetMasterToken, token.ID.String(),
		strings.Join(changed, ", "))

	return token, nil
}

// GetByID retrieves a master token by ID
func (s *Service) GetByID(ctx context.Context, query master_token_get.MasterTokenGetQuery) (*master_token.MasterToken, error) {
	return s.masterTokenRepo.GetByID(ctx, query.AuthContext.AccountID, query.ID)
}

// GetAllByAccountID retrieves all master tokens for an account
func (s *Service) GetAllByAccountID(ctx context.Context, query master_token_list.MasterTokenListQuery) ([]master_token.MasterToken, error) {
	return s.masterTokenRepo.GetAllByAccountID(ctx, query.AuthContext.AccountID)
}

// Delete deletes a master token
func (s *Service) Delete(ctx context.Context, accountID, actorUserID, tokenID uuid.UUID) error {
	if err := s.masterTokenRepo.Delete(ctx, accountID, tokenID); err != nil {
		s.logger.ErrorContext(ctx, "failed to delete master token", "error", err, "token_id", tokenID)
		return err
	}

	s.logger.InfoContext(ctx, "master token deleted", "token_id", tokenID)
	s.audit.Record(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionMasterTokenDelete, audit_event.TargetMasterToken, tokenID.String())
	return nil
}

// ValidateToken validates a master token and returns the token if valid
func (s *Service) ValidateToken(ctx context.Context, tokenValue string) (*master_token.MasterToken, error) {
	tokenHash := master_token.HashToken(tokenValue)

	token, err := s.masterTokenRepo.GetByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, app_errors.ErrMasterTokenInvalid
	}

	if !token.IsActive {
		return nil, app_errors.ErrMasterTokenInactive
	}

	if token.IsExpired() {
		return nil, app_errors.ErrMasterTokenExpired
	}

	// Update last used timestamp asynchronously
	go func() {
		_ = s.masterTokenRepo.UpdateLastUsed(context.Background(), token.ID)
	}()

	return token, nil
}

// GetAdoptionInfo returns adoption file content for a master token (base64-encoded JSON)
// This adoption file allows agents to connect without pre-registration
func (s *Service) GetAdoptionInfo(ctx context.Context, accountID, tokenID uuid.UUID, tokenValue, platformURL, grpcEndpoint string) (string, error) {
	// Fetch token to verify ownership and get details
	token, err := s.masterTokenRepo.GetByID(ctx, accountID, tokenID)
	if err != nil {
		return "", err
	}

	// Create adoption config struct for master token mode
	adoptionConfig := struct {
		MasterToken  string `json:"master_token"`
		AccountID    string `json:"account_id"`
		PlatformURL  string `json:"platform_url"`
		GRPCEndpoint string `json:"grpc_endpoint"`
		CreatedAt    int64  `json:"created_at"`
	}{
		MasterToken:  tokenValue,
		AccountID:    token.AccountID.String(),
		PlatformURL:  platformURL,
		GRPCEndpoint: grpcEndpoint,
		CreatedAt:    time.Now().Unix(),
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(adoptionConfig)
	if err != nil {
		return "", fmt.Errorf("failed to marshal adoption config: %w", err)
	}

	// Encode to base64
	base64Data := base64.StdEncoding.EncodeToString(jsonData)

	return base64Data, nil
}
