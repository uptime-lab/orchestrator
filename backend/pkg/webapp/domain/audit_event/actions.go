package audit_event

// Every action string the platform is capable of recording lives here, and
// nothing writes a literal. That is the point of the file, not tidiness.
//
// The gap it closes: the AuditEvent doc comment used to
// describe the trail in prose — "member, binding, invite and role-setting
// changes, destructive node commands, secret management, account changes,
// terminal opens" — while the actual writers were eight action strings in two
// files. Nothing recorded an access-entry change, a secret, a node delete, a
// terminal open or any device command, so the sentence describing the feature
// was the only place most of it existed. Prose cannot be checked against code;
// a constant list can. Read this file to learn what the trail contains, and add
// a constant here before adding a writer.
//
// Action naming is `<subject>.<verb>`, lowercase, dot-separated. The subject is
// the kind of thing acted upon, not the service that happened to own the code —
// a rename of a Go package must not rename a recorded action, because rows
// already in the table cannot be renamed with it.
const (
	// --- membership, invites, roles -------------------------------------
	// The pre-existing writers (member_service.go, user_service.go). The
	// literals are unchanged: rows written before this file exists must keep
	// matching the constants.
	ActionMemberBindingsReplace = "member.bindings_replace"
	// ActionMemberStatusPrefix is completed with the new status
	// (member.status_active / member.status_disabled), so a status change reads
	// as one action rather than needing the row's detail.
	ActionMemberStatusPrefix = "member.status_"
	ActionMemberRemove       = "member.remove"
	ActionInviteCreate       = "invite.create"
	ActionInviteResend       = "invite.resend"
	ActionInviteRevoke       = "invite.revoke"
	ActionInviteAccept       = "invite.accept"
	// ActionBetaNoticeAcknowledge records acceptance of the operator's notice or
	// terms of use. It is the consent record itself, which is why its write is
	// the one audit write that is NOT best-effort. The string is stored in
	// existing rows and stays as written.
	ActionBetaNoticeAcknowledge = "beta_notice.acknowledge"

	// ActionRoleSettingUpsert / Reset record a change to what a role is allowed
	// to do — a privilege change for every member holding that role, not just
	// one person, which is why it is here and not merely in the access log.
	ActionRoleSettingUpsert = "role_setting.upsert"
	ActionRoleSettingReset  = "role_setting.reset"
	// ActionRoleSettingResetRole reverts EVERY tunable action of a role at once.
	// Separate from a single reset because the blast radius is different and an
	// operator scanning the trail should not have to count rows to see it.
	ActionRoleSettingResetRole = "role_setting.reset_role"

	// --- account -------------------------------------------------------
	ActionAccountUpdate = "account.update"
	// ActionAccountExport records a whole-account data export. It belongs here
	// rather than nowhere for the same reason image.download does: the document
	// leaves the platform, so the request is the only moment the platform can
	// see it happen. The detail carries the section COUNTS and any omitted
	// section — never a row, a name or a value — because "the account was
	// exported" and "the account was exported minus its secrets, because that
	// read failed" are different events and nothing else remembers which.
	ActionAccountExport = "account.export"
	// A tenant asking to be erased, and withdrawing it. The request ROW is
	// transient — cancelling deletes it — so these two events are the only
	// durable record that a customer ever asked. That is why the handler's
	// recorder is a required constructor argument rather than a setter.
	//
	// The customer's free-text reason is deliberately NOT copied into the
	// detail: it can contain anything they typed, including a credential pasted
	// by mistake, and audit_events is on no rewrap list.
	ActionAccountDeletionRequested = "account.deletion_requested"
	ActionAccountDeletionCancelled = "account.deletion_cancelled"
	ActionAccountDelete            = "account.delete"

	// --- shell access ---------------------------------------------------
	// An access entry is a named OS user plus the keys, sudo and certificate
	// principals that let someone in. It is the object that grants a shell and
	// root on every node in an OU, so it is the highest-value thing in the
	// trail: a change here is a change to who can log in to the fleet.
	ActionAccessEntryCreateUser = "access_entry.create_user"
	ActionAccessEntryCreateCA   = "access_entry.create_ca"
	ActionAccessEntryUpdate     = "access_entry.update"
	ActionAccessEntryDelete     = "access_entry.delete"
	ActionAccessEntryKeyAttach  = "access_entry.key_attach"
	ActionAccessEntryKeyDetach  = "access_entry.key_detach"
	// ActionAccessReapplyNode is an operator re-push of a node's effective
	// access config. It changes no stored intent, but it does change what the
	// device believes, so "nobody edited anything and the device changed" has an
	// answer.
	ActionAccessReapplyNode = "access_entry.reapply_node"
	// ActionAccessApplyOffline is the recovery-mode apply: writing the resolved
	// access config into a netbooted node's installed disk. The
	// detail carries the target device — the one caller-supplied input — and
	// never any credential material.
	ActionAccessApplyOffline = "access_entry.apply_offline"

	// SSH keys are inseparable from the entries that reference them: auditing
	// "key X was attached to user Y" while the content of key X could be
	// replaced silently would leave the same hole one level down.
	ActionSSHKeyCreate   = "ssh_key.create"
	ActionSSHKeyGenerate = "ssh_key.generate"
	ActionSSHKeyUpdate   = "ssh_key.update"
	ActionSSHKeyDelete   = "ssh_key.delete"

	// --- secrets --------------------------------------------------------
	// Names and kinds are recorded; VALUES never are. See AuditEvent.Detail.
	ActionSecretCreate = "secret.create"
	ActionSecretUpdate = "secret.update"
	ActionSecretDelete = "secret.delete"
	// ActionSecretUpsert is the create-or-update path used by the secret_upsert
	// workflow block. Distinct from create/update because the caller genuinely
	// does not know which happened, and guessing would make the trail lie.
	ActionSecretUpsert             = "secret.upsert"
	ActionSecretAttach             = "secret.attach"
	ActionSecretDetach             = "secret.detach"
	ActionSecretAttachmentsReplace = "secret.attachments_replace"
	ActionSecretTagAttach          = "secret.tag_attach"
	// ActionSecretValuesRead records that a caller was SERVED plaintext secret
	// material — the only action here that is a read.
	//
	// It exists because secret.read_value gated the plaintext and recorded
	// nothing, so a holder could page the whole account's values, or replay any
	// prior run_script result, and leave no row: the trail could not answer "who
	// saw this credential before it was rotated", which is the first question
	// asked after a leak.
	//
	// The row names the SCOPE and a count, never a value or a name — the detail
	// of an audit event must never carry credential material, and audit_events
	// is on no rewrap list, so anything copied there outlives a key rotation as
	// unreadable plaintext nobody knows to look for.
	ActionSecretValuesRead = "secret.values_read"

	// Node attributes are env-kind secrets behind a per-node API, so they get
	// their own subject — an operator looking for "who changed this node's CI
	// token" is not thinking about the secrets table.
	ActionNodeAttributeSet    = "node_attribute.set"
	ActionNodeAttributeDelete = "node_attribute.delete"

	// --- nodes and devices ----------------------------------------------
	ActionNodeDelete = "node.delete"
	// ActionNodeApprove / Reject record an operator's decision on a device
	// waiting for approval. An approval is the device's admission into the
	// fleet: it counts against the device cap and lets the device run commands.
	ActionNodeApprove = "node.approve"
	ActionNodeReject  = "node.reject"
	// ActionNodeDeviceProfileAssign records declaring (or clearing) a node's
	// hardware equipage — the declaration decides which config.txt overlays a
	// later apply writes to the device, so who changed it matters. The apply
	// itself is covered by the node_command.<slug> choke point.
	ActionNodeDeviceProfileAssign = "node.device_profile_assign"
	// ActionNodeRestore re-creates a deleted node under its ORIGINAL id and
	// token, which is what lets a device still in the field rejoin. It is
	// recorded because it is the one action that can bring a device back onto an
	// account after someone decided it should leave.
	ActionNodeRestore = "node.restore"
	// ActionNodeForget drops the deleted-node record, after which that device
	// cannot be taken back without physical access. Recorded for the same reason
	// as the delete itself: it is irreversible and there is nothing left to read
	// afterwards.
	ActionNodeForget = "node.forget"
	// ActionNodeIdentityReleased records the device REPORTING that it erased
	// its adoption file after a forced delete — the release command's terminal
	// success, stamped on the tombstone because the node row is already gone.
	// System actor: the human's decision is the node.delete row; this is the
	// device's answer, and the detail says "reported_by=device" because the
	// platform cannot verify it.
	ActionNodeIdentityReleased = "node.identity_released"
	// ActionNodeSerialLeaseReleased records the platform clearing the hardware
	// serial of a node that was never admitted and has shown no life for the
	// lease period (QuotaConfig.SerialLeaseDays). System actor: no human asked.
	// The row survives; only the serial is released, so the account that
	// actually holds the device can bind it.
	ActionNodeSerialLeaseReleased = "node.serial_lease_released"
	// ActionNodeTerminalOpen / Close bracket an interactive root shell.
	// Keystrokes are never recorded — the session, not its content.
	ActionNodeTerminalOpen  = "node.terminal_open"
	ActionNodeTerminalClose = "node.terminal_close"

	// ActionNodeCommandPrefix is completed with the command slug
	// (node_command.reboot, node_command.install_os, node_command.run_script…),
	// so "who rebooted my node" is answerable by action alone. The slug is part
	// of the action rather than the detail because the question an operator asks
	// is always about a specific kind of command.
	ActionNodeCommandPrefix = "node_command."

	// --- OS images ------------------------------------------------------
	// ActionImageDownload records a request to take an image OUT of the
	// platform. It is the only image action recorded, and that asymmetry is
	// deliberate: creating, editing and deleting an image record are visible in
	// the image list, while a download leaves no trace anywhere else and hands
	// out a whole-disk snapshot — for a captured image, every file, key and
	// credential that was on the node when it was taken.
	//
	// The row is written when the capability is MINTED, not when the bytes are
	// fetched, because the signed URL outlives the request that produced it and
	// /cdn has no account to attribute a fetch to. So the trail answers "who
	// asked for a copy", which is the question, and not "how many times was it
	// pulled", which it cannot.
	ActionImageDownload = "image.download"

	// --- adoption -------------------------------------------------------
	// A master token lets any device holding it join the account, so minting one
	// is a grant, not a configuration change.
	ActionMasterTokenCreate = "master_token.create"
	ActionMasterTokenUpdate = "master_token.update"
	ActionMasterTokenDelete = "master_token.delete"
	// SSH adoption offers an operator's credentials to a host the platform has
	// never seen. The preflight row is where a host key is accepted on first
	// use, the start row names the target and the auth kind, and the finish
	// row carries the fingerprint the run pinned or observed — so "which key
	// did we trust for this device, and who asked" has an answer. Never the
	// password, the private key or the sudo password; see AuditEvent.Detail.
	ActionNodeSSHPreflight     = "node.ssh_preflight"
	ActionNodeSSHAdoptStart    = "node.ssh_adopt.start"
	ActionNodeSSHAdoptComplete = "node.ssh_adopt.complete"
	ActionNodeSSHAdoptFail     = "node.ssh_adopt.fail"

	// --- federated sign-in ----------------------------------------------
	// A linked IdP account is a credential that survives everything a victim
	// would reach for — a password change, a passkey rotation, a TOTP enrolment
	// and a full session revocation all leave it signing in. Attaching or
	// removing one is therefore a change to who can reach the account, which is
	// what this trail is for.
	//
	// ActionExternalIdentityLinkStart records the ARMING of a link, not its
	// completion, and the distinction is deliberate rather than sloppy. The
	// link itself happens in the PUBLIC OIDC callback, which has a user but no
	// account, while audit_events.account_id is NOT NULL and a user can
	// belong to two accounts — so there is no honest single row to write
	// there. The authenticated POST that starts the round trip does have an
	// account, so that is what is recorded. A trail showing a start with no
	// matching unlink is exactly as much as the platform actually knows.
	ActionExternalIdentityLinkStart = "external_identity.link_start"
	ActionExternalIdentityUnlink    = "external_identity.unlink"

	// --- administrative credential reset --------------------------------
	// The server binary's --reset-password and --reset-totp, run by whoever
	// holds a shell on the host: the only administrative reset a self-hosted
	// install has (the instance-admin console is not part of it). Recorded
	// as the SYSTEM actor — the operator holds no session and no user id —
	// under every account the user is a member of, since each of those
	// accounts' owners is entitled to see that one of their members' sign-in
	// changed. The detail names the route and whether sessions were revoked;
	// never the temporary password, its hash, or the removed secret.
	ActionUserPasswordResetByOperator  = "user.password_reset_by_operator"
	ActionUserTwoFactorResetByOperator = "user.two_factor_reset_by_operator"

	// --- playbooks ------------------------------------------------------
	// A playbook definition decides what runs unattended on the fleet — a cron
	// trigger fires it with nobody watching, and a privileged one can install
	// an OS or run a root script on every node in its scope. Who created,
	// edited, re-scoped or deleted one is therefore trail material in the same
	// way an access entry is. The DISPATCHES a run produces are already covered
	// by the node_command.<slug> choke point; these record changes to the
	// definition itself.
	//
	// The detail carries the playbook's name, status and scope counts ONLY —
	// never the graph, steps or any block parameter. Params can hold literal
	// credentials an author typed into a script or header, and audit_events is
	// on no rewrap list.
	ActionPlaybookCreate = "playbook.create"
	ActionPlaybookUpdate = "playbook.update"
	// ActionPlaybookImport is distinct from create: the content arrived as a
	// portable document (a starter or another account's export) and always
	// lands as a draft with references resolved or cleared.
	ActionPlaybookImport = "playbook.import"
	ActionPlaybookDelete = "playbook.delete"
	// ActionPlaybookRetargetOU records the dedicated OU re-scope route. A scope
	// change alters WHERE the playbook may run and which nodes a cron fire
	// reaches, which is why it is its own event rather than an update detail.
	ActionPlaybookRetargetOU = "playbook.retarget_ou"
	// ActionPlaybookRunStart / RunCancel record a member starting or
	// cancelling a run. The commands a run dispatches are recorded at the
	// node_command.<slug> choke point as the platform's, because the engine
	// dispatches them; these rows are what names the person, and they outlive
	// the execution rows that the run-history retention deletes. Every start
	// writes one (StartExecution); a scheduled fire's is the platform's. The
	// detail carries ids and counts only — never a block parameter.
	ActionPlaybookRunStart  = "playbook.run_start"
	ActionPlaybookRunCancel = "playbook.run_cancel"

	// --- utilities (docs/UTILITIES-PLAN.md) -----------------------------
	// The assignment mutations are recorded here; the apply itself is covered
	// by the node_command.<slug> choke point like every device command. The
	// delete detail states "(retained on devices)" because unassign-is-retain
	// is the semantics an auditor will otherwise assume wrongly.
	ActionUtilityAssignmentCreate = "utility.assignment_created"
	ActionUtilityAssignmentUpdate = "utility.assignment_updated"
	ActionUtilityAssignmentDelete = "utility.assignment_deleted"

	// --- organizational units (docs/ORGANIZATION-PAGE-PLAN.md) ----------
	// An OU update and an account-policy update can dispatch
	// apply_access_config to every node the level reaches, which made them
	// the largest unrecorded mutations in the product until they were added.
	// Detail carries names, the parent, changed field NAMES and the
	// pushed-node count — never a password or any other field VALUE.
	ActionOUCreate            = "ou.create"
	ActionOUUpdate            = "ou.update"
	ActionOUDelete            = "ou.delete"
	ActionAccountPolicyUpdate = "account_policy.update"
	// ActionNodePolicyReset clears every per-node policy override back to
	// inheritance — it can change the node's effective boot target and sshd
	// password policy in one click, which earns it a row.
	ActionNodePolicyReset = "node.policy_reset"
	// ActionNodePolicyUpdate records a per-node override of the boot and sshd
	// policy flags (network_first, boot_to_os, ssh_password_auth): each can
	// change how the device boots or open password logins on it. The detail
	// names the flags set and their new values (inherit/on/off).
	ActionNodePolicyUpdate = "node.policy_update"
	// ActionNodeAssignOU records moving a node between units, which swaps
	// every unit-level access entry, secret and policy the node inherits. The
	// detail names the previous and the new unit id.
	ActionNodeAssignOU = "node.assign_ou"

	// --- device entitlement --------------------------------------------
	// ActionEntitlementApply records a signed entitlement file being applied
	// to the install. The detail names the customer id, kind, cap and expiry
	// — the file's own terms, which are not credential material — never the
	// file itself. Instance-wide in effect, but recorded under the account of
	// the operator who pasted it, because that is who did it.
	ActionEntitlementApply = "entitlement.apply"
	// ActionEntitlementDeviceCapReached records an admission refused at the
	// device cap, under the account that tried to admit. One row per refusal
	// an operator made (a create, an approval, an SSH adoption); a refusal a
	// device drives by connecting is recorded once per device per hour,
	// because the device asks again on its own and its refusals would
	// otherwise fill the audit trail within any retention window.
	ActionEntitlementDeviceCapReached = "entitlement.device_cap_reached"
	// ActionEntitlementAccountDeviceLimitReached records an admission refused
	// at the account's OWN device limit (ACCOUNT_DEVICE_LIMIT or the
	// account's override), under that account, throttled like the device-cap
	// row. The detail carries the account's limit, its admitted count and its
	// plan, never an install-wide number.
	ActionEntitlementAccountDeviceLimitReached = "entitlement.account_device_limit_reached"
)

// Target types. Free-form in the column, constant here for the same reason as
// the actions: a read route filtering on target_type needs the set to be
// knowable without grepping for string literals.
const (
	TargetUser        = "user"
	TargetInvite      = "invite"
	TargetAccount     = "account"
	TargetRole        = "role"
	TargetAccessEntry = "access_entry"
	TargetSSHKey      = "ssh_key"
	TargetSecret      = "secret"
	TargetSecretTag   = "secret_tag"
	TargetNode        = "node"
	TargetOU          = "organizational_unit"
	TargetMasterToken = "master_token"
	// TargetExternalIdentity is the federated link itself. For a link_start the
	// id is the USER's, because no identity row exists yet at that moment.
	TargetExternalIdentity  = "external_identity"
	TargetUtilityAssignment = "utility_assignment"
	// TargetPlaybook is the stored name of the thing the UI calls a playbook.
	// The string follows the table (workflow_templates), not the UI label,
	// because rows already written cannot be renamed with a rebrand.
	TargetPlaybook = "workflow_template"
	// TargetPlaybookExecution is one RUN of a playbook. Separate from
	// TargetPlaybook because the question a reader asks about a run — who
	// replayed its outputs — is not the question they ask about a definition.
	TargetPlaybookExecution = "workflow_execution"
	// TargetNodeAdoptionJob is one SSH adoption run; the node it produces does
	// not exist for most of the run, so the job is what the rows point at. The
	// preflight has no job either and targets the host itself (TargetSSHHost,
	// id "host:port") — the one thing every row about a device that is not
	// yet a node can be joined on.
	TargetNodeAdoptionJob = "node_adoption_job"
	TargetSSHHost         = "ssh_host"
	// TargetEntitlement is the install's device entitlement. There is one per
	// install, so the id is the applied file's customer id — the only name a
	// reader can match against the card.
	TargetEntitlement = "entitlement"
)

// NodeCommandAction returns the action string for dispatching a device command.
func NodeCommandAction(slug string) string {
	return ActionNodeCommandPrefix + slug
}

// MemberStatusAction returns the action string for a membership status change.
func MemberStatusAction(status string) string {
	return ActionMemberStatusPrefix + status
}
