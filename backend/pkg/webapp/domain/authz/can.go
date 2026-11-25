package authz

// Override is a per-account role-permission override (from
// account_role_settings), keyed by role+action. Only tunable actions are
// consulted; others always use the default matrix.
type Override map[overrideKey]bool

type overrideKey struct {
	Role   Role
	Action Action
}

// NewOverride builds an override lookup from (role, action, allowed) triples.
func NewOverride() Override {
	return make(Override)
}

// Set records an override for a tunable action (no-op for non-tunable).
func (o Override) Set(role Role, action Action, allowed bool) {
	if IsTunable(action) {
		o[overrideKey{role, action}] = allowed
	}
}

// Grants reports whether a single role grants an action, applying a per-account
// override when the action is tunable and an override exists for that role.
func Grants(role Role, action Action, overrides Override) bool {
	if IsTunable(action) && overrides != nil {
		if allowed, ok := overrides[overrideKey{role, action}]; ok {
			return allowed
		}
	}
	return roleGrantsDefault(role, action)
}

// GrantsAny reports whether any of the roles grants the action. The caller
// (enforcer) passes only the roles whose binding scope covers the target, so
// this is the final matrix decision.
func GrantsAny(roles []Role, action Action, overrides Override) bool {
	for _, role := range roles {
		if Grants(role, action, overrides) {
			return true
		}
	}
	return false
}

// PermittedActions returns every action granted by the given roles (used to
// build the frontend permission map for one scope).
func PermittedActions(roles []Role, overrides Override) []Action {
	out := make([]Action, 0)
	for _, action := range AllActions() {
		if GrantsAny(roles, action, overrides) {
			out = append(out, action)
		}
	}
	return out
}
