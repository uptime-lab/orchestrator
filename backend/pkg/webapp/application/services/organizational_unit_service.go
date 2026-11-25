package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// EffectiveProvisioning is the resolved, ready-to-apply provisioning input for a
// node: node overrides win, then its OU, then the root OU. Unlike the API-facing
// effective policy, the password and the resolved SSH key content are included
// here for internal use when building agent commands.
type EffectiveProvisioning struct {
	Username string
	Password string
	// PasswordUnreadable reports that the level which owns the password holds
	// one that no configured key decrypts. Password is empty in that case, and
	// the two must be read together: an empty password means "none set", an
	// empty password with this flag means "set but unrecoverable". Resolution
	// STOPS at such a level rather than inheriting the parent's password, so a
	// broken node value never silently provisions the OU's credential instead.
	// Only the command-building path acts on it — see the gRPC dispatch.
	PasswordUnreadable bool
	SSHKey             string // resolved key content ("" if none)
	SSHKeyType         string // "ssh" or "ca" ("" if no key)
	OSImageID          *uuid.UUID

	// EnvVars is the resolved set of environment secrets (kind == "env"), keyed
	// by the ENV var name with the decrypted value. Merged node -> OU -> root
	// with a node-level attachment winning per env name.
	EnvVars map[string]string

	// Secrets is the full effective set of attached secrets (env, credential and
	// opaque), decrypted, for internal use only — never surfaced via the API.
	// De-duplicated by secret ID; node-level attachments win over OU over root.
	Secrets []ResolvedSecret

	// SSHPasswordAuth is the resolved password-login policy for the node. The
	// default is true, preserving the agent's historical behaviour when nothing
	// sets it, so an upgrade never silently closes the password path that is
	// currently the only way into some fleets.
	SSHPasswordAuth bool
	// SSHPasswordAuthManaged reports that some level set the policy explicitly.
	// When false the agent is told not to manage PasswordAuthentication at all,
	// rather than being handed the default: writing the default would force
	// password logins ON for a host whose image or site policy already refuses
	// them — the mirror of the bug this feature exists to remove.
	SSHPasswordAuthManaged bool

	// UsernameSource and OUName exist for the refusal message, not for the
	// command. A provisioning config that resolves a username but no credential
	// is a dead end, and the operator's next question is always the same one:
	// which level put that username there, and where do I set the credential.
	// The commonest shape is a username nobody remembers choosing — the WebUSB
	// flasher's "orchestrator" default, or a unit policy — with no password and
	// no key anywhere on the chain, so a refusal that names only the username
	// sends the reader looking at the node.
	//
	// UsernameSource is empty when no level supplies one; OUName is the node's
	// OWN unit and RootOUName the unit above it, each "" when there is none
	// (an unassigned node, a top-level unit, or a dangling reference, which
	// resolves as unassigned).
	UsernameSource organizational_unit.PolicySource
	OUName         string
	RootOUName     string
}

// ResolvedAccessEntry is one entry in a node's effective access set. Passwords
// are decrypted for internal command-building; HasPassword is the only
// password fact the API exposes.
type ResolvedAccessEntry struct {
	EntryID      uuid.UUID
	Type         string // user | ca
	Username     string // user entries
	Sudo         bool
	SudoNoPasswd bool
	HasPassword  bool
	// AdoptUnmanaged opts into taking over a pre-existing OS user of this name.
	AdoptUnmanaged bool
	Password       string      // decrypted, internal only — never surfaced via the API
	Principals     []string    // ca entries
	KeyIDs         []uuid.UUID // referenced ssh_keys rows
	Lifecycle      string      // active | locked (derived; user entries)
	Source         AccessEntrySource
	// Overridden lists the lower-precedence entries this one shadowed (same
	// username), so the UI can render them struck-through with "overridden by".
	Overridden []AccessEntrySource
}

// AccessEntrySource reports which level supplied an access entry.
type AccessEntrySource struct {
	Level   string // node | ou | root | account
	OUID    *uuid.UUID
	EntryID uuid.UUID
}

// ResolvedSecret is a single decrypted secret in a node's effective set,
// annotated with where it came from (for the effective-secrets endpoints).
type ResolvedSecret struct {
	ID        uuid.UUID
	Name      string
	Kind      string
	Key       string
	Value     string
	Sensitive bool
	Source    ResolvedSecretSource
}

// ResolvedSecretSource reports the attachment that put a secret into the
// effective set: its level (node|ou|root) and whether it arrived directly or
// via a tag. When several attachments reference the same secret, the
// highest-precedence one (node > OU > root; direct > tag within a level) is
// reported.
type ResolvedSecretSource struct {
	Level string     // node | ou | root
	Via   string     // direct | tag
	Tag   string     // set when Via == tag
	OUID  *uuid.UUID // set when Level is ou/root
}

// OrganizationalUnitService is the port the gRPC layer uses to resolve a node's
// effective provisioning inputs from its OU hierarchy.
type OrganizationalUnitService interface {
	ResolveEffectiveProvisioning(ctx context.Context, accountID, nodeID uuid.UUID) (EffectiveProvisioning, error)
	// ResolvePasswordAuthPolicy returns just the resolved ssh_password_auth
	// policy (value, and whether any level set it). Both values are computed by
	// the same function ResolveEffectiveProvisioning uses, so a caller that
	// needs only the access-config hash can skip loading the SSH key, decrypting
	// the node's whole secret set and stamping last_used_at.
	ResolvePasswordAuthPolicy(ctx context.Context, accountID, nodeID uuid.UUID) (auth bool, managed bool, err error)
}

// AccessResolver is the port the gRPC layer uses to resolve a node's effective
// access set when building apply_access_config commands. Kept separate from
// OrganizationalUnitService so the access slice owns its own resolution.
type AccessResolver interface {
	ResolveEffectiveAccess(ctx context.Context, accountID, nodeID uuid.UUID) ([]ResolvedAccessEntry, error)
	// ResolveAccessConfig returns the same set with key content hydrated, ready
	// for the wire. Errors are propagated: an empty config means "lock everyone",
	// so it must never stand in for a failed lookup.
	ResolveAccessConfig(ctx context.Context, accountID, nodeID uuid.UUID, managePasswordAuth, passwordAuthEnabled bool) (AccessConfig, error)
}

// AccessConfig is a node's desired access state, resolved and hydrated.
// Passwords are present in the clear for command building only — this type is
// never serialized into a stored payload or returned by the API.
type AccessConfig struct {
	// Hash fingerprints the desired state for the agent's idempotent no-op
	// check. Excludes password values (see hashAccessConfig) but MUST cover
	// everything else the agent would apply, including the password-auth policy:
	// a change the hash cannot see is a change the agent skips while reporting
	// success.
	Hash  string
	Users []AccessConfigUser
	CAs   []AccessConfigCA
	// ManagePasswordAuth and PasswordAuthEnabled carry the resolved
	// ssh_password_auth policy. They live here rather than being attached later
	// so they are inside the hash.
	ManagePasswordAuth  bool
	PasswordAuthEnabled bool
}

// GrantsALogin reports whether this config leaves anyone able to log in.
//
// A user with no keys is a LOCKED user — that is how lifecycle is derived — so
// counting users answers a different question than "can anyone get in". Keys are
// a login; principals are a login only when a CA is trusted to sign for them.
// This mirrors the agent's own predicate deliberately: the two must agree, or
// the platform refuses configurations the device would accept, or the reverse.
func (c AccessConfig) GrantsALogin() bool {
	caTrusted := len(c.CAs) > 0
	for _, u := range c.Users {
		if u.Locked {
			// A locked account cannot log in by any method, certificates
			// included — the expiry lock covers them all.
			continue
		}
		if len(u.AuthorizedKeys) > 0 {
			return true
		}
		if caTrusted && len(u.Principals) > 0 {
			return true
		}
	}
	return false
}

// WouldLockOutAllAccess reports whether applying this config would leave the
// node with no way in at all.
//
// It only fires when the config CLOSES the password path. Leaving passwords
// enabled means whatever works today keeps working — including accounts the
// platform does not manage and cannot see from here — so refusing then would
// block legitimate configurations on a guess. Same rule as the agent's gate.
func (c AccessConfig) WouldLockOutAllAccess() bool {
	if !c.ManagePasswordAuth || c.PasswordAuthEnabled {
		return false
	}
	return !c.GrantsALogin()
}

// AccessConfigUser is one managed OS user in the desired state. Locked is
// derived from an empty key set, not configured independently.
type AccessConfigUser struct {
	Username       string
	AuthorizedKeys []string
	Sudo           bool
	SudoNoPasswd   bool
	HasPassword    bool
	Password       string
	Locked         bool
	// AdoptUnmanaged opts into taking over a pre-existing OS user of this name.
	// Without it the agent refuses to touch an account it did not create, and
	// the entry is reported as skipped.
	AdoptUnmanaged bool
	// Principals are the certificate principals accepted to log in AS this
	// user (AuthorizedPrincipalsFile). Only effective when a CA is trusted.
	Principals []string
}

// AccessConfigCA is a trusted external CA and the principals it may vouch for.
type AccessConfigCA struct {
	PublicKey  string
	Principals []string
}
