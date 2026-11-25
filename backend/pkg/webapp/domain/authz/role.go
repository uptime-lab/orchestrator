package authz

// Role is an RBAC role a user holds within an account, either account-wide or
// scoped to an organizational unit (see role_binding). The permission matrix
// mapping roles to actions arrives with the enforcement layer; until then the
// only privileged distinction in effect is super_admin (member management).
type Role string

const (
	// RoleViewer grants read-only access (secret values excluded).
	RoleViewer Role = "viewer"
	// RoleManager grants viewer plus non-privileged actions (reboot,
	// status-check, running playbooks made of non-privileged blocks).
	RoleManager Role = "manager"
	// RoleAdmin grants everything within its scope, including destructive
	// actions; account-global resources stay read-only.
	RoleAdmin Role = "admin"
	// RoleSuperAdmin grants everything including member, invite, master
	// token, image, SSH key and account-policy management. It can only be
	// bound account-wide (never to an OU).
	RoleSuperAdmin Role = "super_admin"
)

// IsValid reports whether the role is one of the defined roles.
func (r Role) IsValid() bool {
	switch r {
	case RoleViewer, RoleManager, RoleAdmin, RoleSuperAdmin:
		return true
	}
	return false
}

// CanBindToOU reports whether the role may be scoped to an organizational
// unit. super_admin is account-wide by definition.
func (r Role) CanBindToOU() bool {
	return r != RoleSuperAdmin
}
