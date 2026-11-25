package account_role_setting

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/google/uuid"
)

// AccountRoleSetting is a per-account override of the default role→action
// matrix, edited by super_admin. Only actions on the tunable whitelist (owned
// by domain/authz once the matrix lands) may be overridden; member.*,
// account.* and super_admin's own permissions are never tunable.
type AccountRoleSetting struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	Role      authz.Role
	Action    string
	Allowed   bool
	UpdatedBy uuid.UUID
	UpdatedAt time.Time
}

// NewAccountRoleSetting creates a role-permission override.
func NewAccountRoleSetting(accountID uuid.UUID, role authz.Role, action string, allowed bool, updatedBy uuid.UUID) *AccountRoleSetting {
	return &AccountRoleSetting{
		ID:        uuid.New(),
		AccountID: accountID,
		Role:      role,
		Action:    action,
		Allowed:   allowed,
		UpdatedBy: updatedBy,
		UpdatedAt: time.Now(),
	}
}
