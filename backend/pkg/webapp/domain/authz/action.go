package authz

// Action is a permission-checked operation. Actions are grouped by resource;
// read actions are the least privileged, management/destructive actions the
// most. The enforcement layer maps each mutating endpoint (and each workflow
// block) to one of these.
type Action string

const (
	// Node read + non-destructive.
	ActionNodeRead        Action = "node.read"
	ActionNodeReboot      Action = "node.reboot"
	ActionNodeStatusCheck Action = "node.status_check"
	ActionNodeSetHostname Action = "node.set_hostname"
	// Node destructive / privileged.
	ActionNodeCreate         Action = "node.create" // manually create a node record
	ActionNodeShutdown       Action = "node.shutdown"
	ActionNodeUpdate         Action = "node.update" // edit name / ssh key / default creds
	ActionNodeInstallOS      Action = "node.install_os"
	ActionNodeDelete         Action = "node.delete"
	ActionNodeApprove        Action = "node.approve"
	ActionNodeEEPROMFlash    Action = "node.eeprom_flash"
	ActionNodeRebootRecovery Action = "node.reboot_recovery"
	ActionNodeFileWrite      Action = "node.file_write" // RCE-equivalent root file write
	ActionNodeReplace        Action = "node.replace"
	ActionNodeUpdateAgent    Action = "node.update_agent"
	ActionNodeAttributes     Action = "node.attributes_write"
	ActionNodeAssignOU       Action = "node.assign_ou"
	ActionNodeProvisioningSD Action = "node.provisioning_sd" // per-node SD boot bundle (own node, OU-scoped)
	ActionNodeSSHAdopt       Action = "node.ssh_adopt"       // adopt a running device over SSH (installs agent, EEPROM)
	// Credential-bearing reads (excluded from viewer even though they are GETs).
	ActionNodeCredentials Action = "node.credentials_read"
	ActionNodeTerminal    Action = "node.terminal"

	// Secrets. Read is the METADATA gate (names, keys, kinds, tags, attachment
	// targets); ReadValue is the separate, stricter gate on plaintext. Listing
	// alone is worth protecting: a secret's name IS its {{secrets.<name>}}
	// identifier, and its attachments say which node holds which credential.
	ActionSecretRead      Action = "secret.read"
	ActionSecretReadValue Action = "secret.read_value"
	ActionSecretManage    Action = "secret.manage"

	// Account-global resources.
	ActionImageRead         Action = "image.read"
	ActionImageManage       Action = "image.manage"
	ActionSSHKeyRead        Action = "sshkey.read"
	ActionSSHKeyManage      Action = "sshkey.manage"
	ActionMasterTokenManage Action = "master_token.manage"
	ActionProvisioningSD    Action = "provisioning.sd"
	ActionAccountPolicy     Action = "account_policy.manage"
	ActionAccountUpdate     Action = "account.update"
	ActionAccountDelete     Action = "account.delete"
	// ActionAccountExport gates the whole-account data export. It is separate
	// from account.update because it is a READ, and separate from the per-domain
	// read actions because the document is the union of all of them: every
	// member's email address, every node, every non-sensitive variable's value.
	// No role below super_admin lists it, so super_admin's blanket grant is the
	// only thing that satisfies it — which is the intent, not an oversight.
	ActionAccountExport Action = "account.export"
	// ActionAuditRead gates reading the account's audit trail. Same posture as
	// account.export: no role below super_admin lists it, so only super_admin's
	// blanket grant satisfies it. The trail names every member who touched
	// anything privileged — who deleted a node, who opened a terminal, who
	// changed an access entry — which is oversight material, not a per-OU read.
	// Widening it below super_admin is a product decision, not a matrix edit
	// someone makes in passing.
	ActionAuditRead Action = "audit.read"
	// ActionEntitlementManage gates reading and applying the install's device
	// entitlement. Same posture as audit.read: no role below super_admin
	// lists it. The card is about the INSTALL — the file names a customer and
	// a cap that every tenant shares — and applying a file changes what the
	// whole install admits, which is not a per-account right.
	ActionEntitlementManage Action = "entitlement.manage"

	// Organizational units.
	ActionOURead   Action = "ou.read"
	ActionOUManage Action = "ou.manage"

	// Access entries. Granting an OS shell on hardware is a higher privilege
	// than curating the SSH-key inventory, so it is its own action rather than
	// reusing sshkey.manage.
	ActionAccessManage Action = "access.manage"

	// Playbooks. run_privileged/author_privileged cover blocks that can run
	// scripts, install OS, write files, flash EEPROM, upsert secrets, etc.
	ActionPlaybookRead             Action = "playbook.read"
	ActionPlaybookRun              Action = "playbook.run"
	ActionPlaybookRunPrivileged    Action = "playbook.run_privileged"
	ActionPlaybookAuthor           Action = "playbook.author"
	ActionPlaybookAuthorPrivileged Action = "playbook.author_privileged"

	// Utilities (docs/UTILITIES-PLAN.md). Manage ranks with the privileged
	// playbook actions, not with sshkey.manage: an assignment installs
	// root-level software on every node it reaches, and the fan-out makes it
	// strictly more powerful than a single privileged run.
	ActionUtilityRead   Action = "utility.read"
	ActionUtilityManage Action = "utility.manage"

	// Per-user personal view (any active member).
	ActionFavoriteSet Action = "favorite.set"

	// Member/invite/role-setting management. Never tunable; enforced in the
	// member service.
	ActionMemberManage Action = "member.manage"
)

// anyScopeReadActions are catalog reads an OU-scoped binding grants at the
// ACCOUNT level (nil target). The four cover exactly what an OU-scoped member
// needs to interpret their own subtree: the unit tree itself (the list handler
// filters it to the allowed subtree afterwards), the image and SSH-key
// catalogs OU policies reference by id, and the personal favorites view.
// Everything else keeps the strict rule — a nil target is covered only by an
// account-wide binding — and every MUTATION does regardless of this set.
// Widening this set widens what an OU-scoped member can enumerate account-wide;
// treat an addition as a product decision, not a matrix edit.
var anyScopeReadActions = map[Action]bool{
	ActionOURead:      true,
	ActionImageRead:   true,
	ActionSSHKeyRead:  true,
	ActionFavoriteSet: true,
	// Opened because every utility route carried the strict
	// nil-target gate, so an OU-scoped member lost the ENTIRE Utilities
	// surface while docs/UTILITIES-PLAN.md §3.8 promises OU-scoped holders can
	// operate within their scope. This only opens the door; the list handlers
	// narrow via AllowedOUs (account-wide assignments stay visible to
	// account-wide holders only — the doctrine on AllowedOUs), and
	// utility.manage keeps the strict rule with per-unit checks in the
	// handler.
	ActionUtilityRead: true,
}

// IsAnyScopeRead reports whether holding the action under ANY binding scope
// satisfies an account-level (nil-target) check.
func IsAnyScopeRead(action Action) bool {
	return anyScopeReadActions[action]
}

// AllActions lists every action, used to expand the permission map returned
// to the frontend.
func AllActions() []Action {
	return []Action{
		ActionNodeRead, ActionNodeReboot, ActionNodeStatusCheck, ActionNodeSetHostname,
		ActionNodeCreate, ActionNodeShutdown, ActionNodeUpdate, ActionNodeInstallOS, ActionNodeDelete, ActionNodeApprove,
		ActionNodeEEPROMFlash, ActionNodeRebootRecovery, ActionNodeFileWrite, ActionNodeReplace,
		ActionNodeUpdateAgent, ActionNodeAttributes, ActionNodeAssignOU, ActionNodeProvisioningSD, ActionNodeSSHAdopt,
		ActionNodeCredentials, ActionNodeTerminal,
		ActionSecretRead, ActionSecretReadValue, ActionSecretManage,
		ActionImageRead, ActionImageManage, ActionSSHKeyRead, ActionSSHKeyManage,
		ActionMasterTokenManage, ActionProvisioningSD, ActionAccountPolicy,
		ActionAccountUpdate, ActionAccountDelete, ActionAccountExport, ActionAuditRead,
		ActionEntitlementManage,
		ActionOURead, ActionOUManage, ActionAccessManage,
		ActionPlaybookRead, ActionPlaybookRun, ActionPlaybookRunPrivileged,
		ActionPlaybookAuthor, ActionPlaybookAuthorPrivileged,
		ActionUtilityRead, ActionUtilityManage,
		ActionFavoriteSet, ActionMemberManage,
	}
}

// TunableActions is the whitelist of actions a super_admin may override per
// account via account_role_settings. member.*, account.* and super_admin's
// own permissions are never tunable (no self-lockout, no delegation of
// privilege management).
var tunableActions = map[Action]bool{
	ActionNodeTerminal:   true,
	ActionOUManage:       true,
	ActionProvisioningSD: true,
	ActionNodeReboot:     true,
	ActionPlaybookAuthor: true,
}

// IsTunable reports whether an action may be overridden per account.
func IsTunable(action Action) bool {
	return tunableActions[action]
}

// TunableActionList returns the tunable actions in a stable order (for the
// settings UI).
func TunableActionList() []Action {
	// Explicit order so the UI is deterministic.
	return []Action{
		ActionNodeReboot,
		ActionNodeTerminal,
		ActionProvisioningSD,
		ActionOUManage,
		ActionPlaybookAuthor,
	}
}

// TunableRoles returns the roles whose tunable permissions may be overridden
// per account (super_admin is never tunable).
func TunableRoles() []Role {
	return []Role{RoleViewer, RoleManager, RoleAdmin}
}
