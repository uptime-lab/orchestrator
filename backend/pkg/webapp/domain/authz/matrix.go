package authz

// defaultMatrix is the built-in role→action grant table. A super_admin is
// granted everything implicitly (see roleGrants). Account overrides
// (account_role_settings) are layered on top of this in Can via the override
// map — only for tunable actions.
//
// Scope note: viewer/manager/admin evaluate against the binding's scope
// (account-wide or an OU subtree); account-global actions (images, tokens,
// account settings, OU tree) are only ever granted to super_admin, so their
// scope is moot.
var defaultMatrix = map[Role]map[Action]bool{
	RoleViewer: {
		ActionNodeRead:     true,
		ActionImageRead:    true,
		ActionSSHKeyRead:   true,
		ActionOURead:       true,
		ActionPlaybookRead: true,
		ActionUtilityRead:  true,
		ActionFavoriteSet:  true,
	},
	RoleManager: {
		// viewer grants are merged in roleGrants; only the additions here.
		ActionNodeReboot:      true,
		ActionNodeStatusCheck: true,
		ActionNodeSetHostname: true,
		ActionPlaybookRun:     true,
		ActionPlaybookAuthor:  true,
	},
	RoleAdmin: {
		// manager grants are merged in roleGrants; destructive node actions,
		// secrets and privileged playbooks are added here. Account-global
		// resources stay read-only (managed by super_admin only).
		ActionNodeCreate:               true,
		ActionNodeShutdown:             true,
		ActionNodeUpdate:               true,
		ActionNodeInstallOS:            true,
		ActionNodeDelete:               true,
		ActionNodeApprove:              true,
		ActionNodeEEPROMFlash:          true,
		ActionNodeRebootRecovery:       true,
		ActionNodeFileWrite:            true,
		ActionNodeReplace:              true,
		ActionNodeUpdateAgent:          true,
		ActionNodeAttributes:           true,
		ActionNodeAssignOU:             true,
		ActionNodeProvisioningSD:       true,
		ActionNodeSSHAdopt:             true,
		ActionNodeCredentials:          true,
		ActionNodeTerminal:             true,
		ActionSecretRead:               true,
		ActionSecretReadValue:          true,
		ActionSecretManage:             true,
		ActionAccessManage:             true,
		ActionPlaybookRunPrivileged:    true,
		ActionPlaybookAuthorPrivileged: true,
		// utility.manage sits at admin with the privileged playbook actions —
		// an assignment installs root software on every node it reaches.
		ActionUtilityManage: true,
	},
}

// roleRank orders roles so a higher role inherits every grant of the lower
// ones (viewer ⊂ manager ⊂ admin ⊂ super_admin).
var roleRank = map[Role]int{
	RoleViewer:     0,
	RoleManager:    1,
	RoleAdmin:      2,
	RoleSuperAdmin: 3,
}

// roleGrantsDefault reports whether the role grants the action by the built-in
// matrix (cumulative across the role hierarchy). super_admin grants all.
func roleGrantsDefault(role Role, action Action) bool {
	if role == RoleSuperAdmin {
		return true
	}
	rank := roleRank[role]
	for r, actions := range defaultMatrix {
		if roleRank[r] <= rank && actions[action] {
			return true
		}
	}
	return false
}
