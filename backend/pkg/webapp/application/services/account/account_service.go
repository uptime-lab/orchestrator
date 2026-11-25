package account

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_delete"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/account_update"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/google/uuid"
)

// Service implements the AccountService interface
type Service struct {
	accountRepo     repository.AccountRepository
	userRepo        repository.UserRepository
	memberRepo      repository.AccountMemberRepository
	bindingRepo     repository.RoleBindingRepository
	inviteRepo      repository.AccountInviteRepository
	roleSettingRepo repository.AccountRoleSettingRepository
	// twoFactorPolicy caches the per-account 2FA requirement on the request hot
	// path; this service invalidates it when the flag changes. Optional.
	twoFactorPolicy twoFactorPolicyInvalidator
	audit           *audit.Recorder
	logger          *slog.Logger
}

// twoFactorPolicyInvalidator drops a cached per-account policy answer.
type twoFactorPolicyInvalidator interface {
	Invalidate(accountID uuid.UUID)
}

// SetTwoFactorPolicy wires the policy cache so a toggle takes effect at once.
func (s *Service) SetTwoFactorPolicy(p twoFactorPolicyInvalidator) {
	s.twoFactorPolicy = p
}

// NewAccountService creates a new AccountService instance
func NewAccountService(
	accountRepo repository.AccountRepository,
	userRepo repository.UserRepository,
	memberRepo repository.AccountMemberRepository,
	bindingRepo repository.RoleBindingRepository,
	inviteRepo repository.AccountInviteRepository,
	roleSettingRepo repository.AccountRoleSettingRepository,
	auditRecorder *audit.Recorder,
	logger *slog.Logger,
) *Service {
	if auditRecorder == nil {
		panic("account.Service requires an audit recorder")
	}
	return &Service{
		accountRepo:     accountRepo,
		userRepo:        userRepo,
		memberRepo:      memberRepo,
		bindingRepo:     bindingRepo,
		inviteRepo:      inviteRepo,
		roleSettingRepo: roleSettingRepo,
		audit:           auditRecorder,
		logger:          logger.With("service", "account_service"),
	}
}

// CreateAccount creates a new account
func (s *Service) CreateAccount(ctx context.Context, accountName string) (*account.Account, error) {
	// Create the account in the repository
	newAccount := account.NewAccount(accountName)

	// Save the account to the repository
	if err := s.accountRepo.Create(ctx, newAccount); err != nil {
		s.logger.Error("failed to create account", "error", err)
		return nil, err
	}

	return newAccount, nil
}

// GetAccount retrieves an account by its ID
func (s *Service) GetAccount(ctx context.Context, id uuid.UUID) (*account.Account, error) {
	s.logger.Info("getting account", "id", id)
	return s.accountRepo.GetByID(ctx, id)
}

// UpdateAccount updates an existing account with data from a command
func (s *Service) UpdateAccount(ctx context.Context, cmd account_update.AccountUpdateCommand) (*account.Account, error) {
	s.logger.Info("updating account", "id", cmd.ID, "account_id", cmd.AuthContext.AccountID, "user_id", cmd.AuthContext.UserID)

	// Validate that the user has access to this account
	if cmd.AuthContext.AccountID != cmd.ID {
		s.logger.Warn("unauthorized account update attempt",
			"account_id", cmd.ID,
			"user_account_id", cmd.AuthContext.AccountID,
			"user_id", cmd.AuthContext.UserID)
		return nil, app_errors.ErrUnauthorized
	}

	// Fetch the existing account
	acc, err := s.accountRepo.GetByID(ctx, cmd.ID)
	if err != nil {
		s.logger.Error("failed to get account for update", "id", cmd.ID, "error", err)
		return nil, app_errors.ErrAccountNotFound
	}

	// Update account fields — a nil Name means "unchanged", so a settings
	// toggle no longer writes back the name its page captured at mount.
	if cmd.Name != nil {
		acc.Name = *cmd.Name
	}
	if cmd.RequireTwoFactor != nil {
		acc.RequireTwoFactor = *cmd.RequireTwoFactor
	}
	if cmd.IdentifyDurationSec != nil {
		v := *cmd.IdentifyDurationSec
		if v < 10 || v > 3600 {
			return nil, app_errors.InvalidRequest("invalid_identify_duration",
				fmt.Errorf("identify_duration_sec must be between 10 and 3600"))
		}
		acc.IdentifyDurationSec = v
	}
	acc.UpdatedAt = time.Now()

	// Save the updated account
	err = s.accountRepo.Update(ctx, acc)
	if err != nil {
		s.logger.Error("failed to update account", "id", cmd.ID, "error", err)
		return nil, app_errors.ErrFailedToUpdateAccount
	}

	// Drop the cached policy so toggling the 2FA requirement bites at once
	// rather than within the cache TTL.
	if s.twoFactorPolicy != nil {
		s.twoFactorPolicy.Invalidate(acc.ID)
	}

	// require_two_factor is the field that matters here: turning it OFF removes an
	// enrollment requirement from every member at once, and the endpoint's own
	// step-up guard leaves no record of who did it.
	detail := fmt.Sprintf("name=%q", acc.Name)
	if cmd.RequireTwoFactor != nil {
		detail += fmt.Sprintf(" require_two_factor=%t", *cmd.RequireTwoFactor)
	}
	if cmd.IdentifyDurationSec != nil {
		detail += fmt.Sprintf(" identify_duration_sec=%d", *cmd.IdentifyDurationSec)
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionAccountUpdate, audit_event.TargetAccount, acc.ID.String(), detail)

	return acc, nil
}

// DeleteAccount deletes an account by its ID
func (s *Service) DeleteAccount(ctx context.Context, cmd account_delete.AccountDeleteCommand) error {
	s.logger.Info("deleting account", "id", cmd.ID, "account_id", cmd.AuthContext.AccountID, "user_id", cmd.AuthContext.UserID)

	// Validate that the user has access to this account
	if cmd.AuthContext.AccountID != cmd.ID {
		s.logger.Warn("unauthorized account delete attempt",
			"account_id", cmd.ID,
			"user_account_id", cmd.AuthContext.AccountID,
			"user_id", cmd.AuthContext.UserID)
		return app_errors.ErrUnauthorized
	}

	// The instance operator's account is never deleted here: the role is
	// derived (the oldest account), so deleting it promotes a tenant. Fails
	// CLOSED — a lookup that errors refuses the delete, because a delete
	// cannot be taken back and a refused one can be retried.
	operatorID, err := s.accountRepo.InstanceOperatorAccountID(ctx)
	if err != nil {
		return fmt.Errorf("resolving the instance operator before deleting an account: %w", err)
	}
	if operatorID == cmd.ID {
		s.logger.Warn("refused deleting the instance operator's account",
			"account_id", cmd.ID, "user_id", cmd.AuthContext.UserID)
		return &InstanceOperatorAccountError{AccountID: cmd.ID}
	}

	// Delete the account's memberships, bindings, invites and role settings —
	// NEVER user rows: identities are global and may belong to other
	// accounts. An identity left with zero memberships simply cannot select
	// any account.
	if err := s.bindingRepo.DeleteByAccountID(ctx, cmd.ID); err != nil {
		s.logger.Error("failed to delete role bindings for account", "account_id", cmd.ID, "error", err)
		return err
	}
	if err := s.inviteRepo.DeleteByAccountID(ctx, cmd.ID); err != nil {
		s.logger.Error("failed to delete invites for account", "account_id", cmd.ID, "error", err)
		return err
	}
	if err := s.roleSettingRepo.DeleteByAccountID(ctx, cmd.ID); err != nil {
		s.logger.Error("failed to delete role settings for account", "account_id", cmd.ID, "error", err)
		return err
	}
	if err := s.memberRepo.DeleteByAccountID(ctx, cmd.ID); err != nil {
		s.logger.Error("failed to delete memberships for account", "account_id", cmd.ID, "error", err)
		return err
	}

	// Delete the account
	if err := s.accountRepo.Delete(ctx, cmd.ID); err != nil {
		s.logger.Error("failed to delete account", "account_id", cmd.ID, "error", err)
		return err
	}

	// Recorded AFTER the account row is gone, and it deliberately outlives the
	// tenant: audit_events has no foreign key to accounts, so this row survives as
	// the only evidence the deletion happened and who asked for it. The account it
	// belongs to can no longer read it — an operator console outside this tree
	// is the reader that matters for a self-service teardown.
	s.audit.Record(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionAccountDelete, audit_event.TargetAccount, cmd.ID.String())

	s.logger.Info("successfully deleted account", "account_id", cmd.ID)
	return nil
}
