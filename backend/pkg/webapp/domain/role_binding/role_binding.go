package role_binding

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/google/uuid"
)

// RoleBinding grants a user a role within an account, IAM-style. OUID nil
// means the role applies account-wide; otherwise it covers the OU and its
// children. A user may hold several bindings in one account (e.g. viewer
// account-wide plus admin of one OU).
type RoleBinding struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	UserID    uuid.UUID
	Role      authz.Role
	OUID      *uuid.UUID
	CreatedAt time.Time
	// CreatedBy is the user who granted the binding; nil for bindings created
	// by the system (e.g. the legacy-user migration or signup).
	CreatedBy *uuid.UUID
}

// NewRoleBinding creates a role binding.
func NewRoleBinding(accountID, userID uuid.UUID, role authz.Role, ouID, createdBy *uuid.UUID) *RoleBinding {
	return &RoleBinding{
		ID:        uuid.New(),
		AccountID: accountID,
		UserID:    userID,
		Role:      role,
		OUID:      ouID,
		CreatedAt: time.Now(),
		CreatedBy: createdBy,
	}
}

// IsAccountWide reports whether the binding applies to the whole account.
func (b *RoleBinding) IsAccountWide() bool {
	return b.OUID == nil
}
