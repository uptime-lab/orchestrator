package repository

import (
	"context"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/google/uuid"
)

// AccountRoleSettingRepository persists per-account role-permission overrides.
type AccountRoleSettingRepository interface {
	// Upsert creates or updates the override for (account, role, action).
	Upsert(ctx context.Context, setting *account_role_setting.AccountRoleSetting) error
	// ListByAccountID returns all overrides of an account.
	ListByAccountID(ctx context.Context, accountID uuid.UUID) ([]account_role_setting.AccountRoleSetting, error)
	// Delete removes the override for (account, role, action), reverting to
	// the built-in default.
	Delete(ctx context.Context, accountID uuid.UUID, role, action string) error
	// DeleteByAccountID removes all overrides of an account.
	DeleteByAccountID(ctx context.Context, accountID uuid.UUID) error
}
