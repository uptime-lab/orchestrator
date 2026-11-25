package authz

import (
	"context"
	"fmt"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/account_role_setting"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	"github.com/google/uuid"
)

// ListRoleSettings returns the account's role-permission overrides.
func (e *Enforcer) ListRoleSettings(ctx context.Context, accountID uuid.UUID) ([]account_role_setting.AccountRoleSetting, error) {
	return e.roleSettingRepo.ListByAccountID(ctx, accountID)
}

// UpsertRoleSetting validates and stores a role-permission override. Only
// whitelisted (tunable) actions may be overridden, and only for the tunable
// roles (viewer/manager/admin — never super_admin). It invalidates every
// cached resolution for the account so the change takes effect at once.
func (e *Enforcer) UpsertRoleSetting(ctx context.Context, accountID, updatedBy uuid.UUID, role authz.Role, action authz.Action, allowed bool) error {
	if !role.IsValid() || role == authz.RoleSuperAdmin {
		return fmt.Errorf("%w: role %q is not tunable", app_errors.ErrInvalidRole, role)
	}
	if !authz.IsTunable(action) {
		return fmt.Errorf("%w: action %q is not tunable", app_errors.ErrForbidden, action)
	}
	setting := account_role_setting.NewAccountRoleSetting(accountID, role, string(action), allowed, updatedBy)
	if err := e.roleSettingRepo.Upsert(ctx, setting); err != nil {
		return err
	}
	e.invalidateAccount(accountID)
	// A privilege change for EVERY member holding the role, not for one person —
	// which is why the target is the role and not a user, and why this belongs in
	// the trail even though the endpoint already demands a step-up.
	e.audit.RecordDetail(ctx, audit.UserActor(accountID, updatedBy),
		audit_event.ActionRoleSettingUpsert, audit_event.TargetRole, string(role),
		fmt.Sprintf("%s allowed=%t", action, allowed))
	return nil
}

// ResetRoleSetting removes a single override, reverting the (role, action) to
// its built-in default.
func (e *Enforcer) ResetRoleSetting(ctx context.Context, accountID, actorUserID uuid.UUID, role authz.Role, action authz.Action) error {
	if err := e.roleSettingRepo.Delete(ctx, accountID, string(role), string(action)); err != nil {
		return err
	}
	e.invalidateAccount(accountID)
	e.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionRoleSettingReset, audit_event.TargetRole, string(role),
		string(action))
	return nil
}

// ResetRole removes all overrides for a role, reverting every tunable action
// to its built-in default.
func (e *Enforcer) ResetRole(ctx context.Context, accountID, actorUserID uuid.UUID, role authz.Role) error {
	if !role.IsValid() || role == authz.RoleSuperAdmin {
		return fmt.Errorf("%w: role %q is not tunable", app_errors.ErrInvalidRole, role)
	}
	for _, action := range authz.TunableActionList() {
		if err := e.roleSettingRepo.Delete(ctx, accountID, string(role), string(action)); err != nil {
			return err
		}
	}
	e.invalidateAccount(accountID)
	// One row for the whole revert, not one per action: the operator performed a
	// single action and per-action rows would bury everything around them.
	e.audit.RecordDetail(ctx, audit.UserActor(accountID, actorUserID),
		audit_event.ActionRoleSettingResetRole, audit_event.TargetRole, string(role),
		fmt.Sprintf("all %d tunable actions reverted to defaults", len(authz.TunableActionList())))
	return nil
}

// RolePermission is one tunable action's state for a role.
type RolePermission struct {
	Action     string `json:"action"`
	Default    bool   `json:"default"`    // built-in matrix value
	Allowed    bool   `json:"allowed"`    // effective value (override or default)
	Overridden bool   `json:"overridden"` // an explicit override exists
}

// RolePermissions groups a role's tunable permissions.
type RolePermissions struct {
	Role        string           `json:"role"`
	Permissions []RolePermission `json:"permissions"`
}

// RolePermissionsView returns, for each tunable role, every tunable action
// with its default, effective value and whether it is overridden — the
// self-contained shape the settings UI renders.
func (e *Enforcer) RolePermissionsView(ctx context.Context, accountID uuid.UUID) ([]RolePermissions, error) {
	settings, err := e.roleSettingRepo.ListByAccountID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	overrides := authz.NewOverride()
	overridden := make(map[string]bool) // "role|action"
	for _, s := range settings {
		overrides.Set(s.Role, authz.Action(s.Action), s.Allowed)
		overridden[string(s.Role)+"|"+s.Action] = true
	}

	out := make([]RolePermissions, 0, len(authz.TunableRoles()))
	for _, role := range authz.TunableRoles() {
		rp := RolePermissions{Role: string(role)}
		for _, action := range authz.TunableActionList() {
			rp.Permissions = append(rp.Permissions, RolePermission{
				Action:     string(action),
				Default:    authz.Grants(role, action, nil),
				Allowed:    authz.Grants(role, action, overrides),
				Overridden: overridden[string(role)+"|"+string(action)],
			})
		}
		out = append(out, rp)
	}
	return out, nil
}

// invalidateAccount drops every cached resolution belonging to the account.
func (e *Enforcer) invalidateAccount(accountID uuid.UUID) {
	prefix := accountID.String() + "|"
	e.mu.Lock()
	for k := range e.cache {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(e.cache, k)
		}
	}
	e.mu.Unlock()
}
