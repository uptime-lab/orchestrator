package access_entry

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_key"
	"github.com/google/uuid"
)

// Type is the kind of access an entry grants.
type Type string

const (
	// TypeUser grants a named OS user: a set of SSH keys, an optional sudo flag
	// and an optional password. Its lifecycle is DERIVED from the key set (see
	// DesiredState), never configured directly.
	TypeUser Type = "user"
	// TypeCA trusts an external certificate authority: the referenced CA public
	// key is added to the node's trusted-CA set and gated by Principals. The
	// platform issues nothing — it only trusts.
	TypeCA Type = "ca"
)

var validTypes = []Type{TypeUser, TypeCA}

// IsValidType reports whether t is a known access-entry type.
func IsValidType(t string) bool {
	for _, v := range validTypes {
		if string(v) == t {
			return true
		}
	}
	return false
}

// Domain validation errors.
var (
	// ErrInvalidTarget is returned when an entry targets more than one scope.
	// A valid entry targets a node XOR an OU, or neither (account-level).
	ErrInvalidTarget = errors.New("access entry must target at most one of an organizational unit or a node")
	// ErrInvalidType is returned for an unknown entry type.
	ErrInvalidType = errors.New("access entry type must be 'user' or 'ca'")
	// ErrUsernameRequired is returned when a user entry has no username.
	ErrUsernameRequired = errors.New("a user access entry requires a username")
	// ErrUsernameInvalid is returned when a username is not a portable POSIX name.
	ErrUsernameInvalid = errors.New("access entry username must match [a-z_][a-z0-9_-]* (max 32 chars)")
	// ErrUsernameReserved is returned for usernames the platform must never manage.
	ErrUsernameReserved = errors.New("access entry username is reserved")

	ErrPrincipalInvalid = errors.New("certificate principal contains characters the device will reject")
	// ErrAdoptNeedsCredential is returned when an entry takes over an existing
	// account (adopt_unmanaged) with neither a key nor a password. On the
	// device that clears the account's password, locks it and ends its
	// sessions — the stock account an operator may be logged in with.
	ErrAdoptNeedsCredential = errors.New("taking over an existing account needs a key or a password in the same entry: without one the device clears the account's password and locks it")
	// ErrCAKeyRequired is returned when a ca entry does not reference exactly
	// one CA-type key.
	ErrCAKeyRequired = errors.New("a ca access entry requires exactly one CA key")
	// ErrKeyTypeMismatch is returned when a referenced key's type does not match
	// the entry: user entries take "ssh" keys, ca entries take a "ca" key.
	ErrKeyTypeMismatch = errors.New("referenced key type does not match the access entry type")
	// ErrUnknownKey is returned when a referenced key id is not in the account.
	ErrUnknownKey = errors.New("access entry references an unknown SSH key")
)

// principalRe is the exact charset the AGENT accepts for a certificate
// principal (agent/pkg/application/handlers/access_config.go). It MUST stay in
// sync: a principal the agent rejects fails the ENTIRE apply_access_config at
// Phase 0, so one bad string (e.g. a space, "alice bob") on an account-wide entry
// stops every later key detach and user removal from landing, fleet-wide and with
// no attempt cap — validating at save time turns that silent, permanent failure
// into a 400 the operator sees immediately.
var principalRe = regexp.MustCompile(`^[A-Za-z0-9._@-]+$`)

// usernameRe is the portable POSIX username charset. The stricter guards
// (UID < 1000, the agent's own user) are enforced agent-side where the host is
// known; here we only reject shapes that can never be valid.
var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// reservedUsernames may never be managed through an access entry. root is the
// obvious one; the agent additionally refuses low UIDs and its own account.
var reservedUsernames = map[string]struct{}{
	"root": {},
}

// AccessEntry is a declarative access record on a target (node, OU, or the
// account as a whole). A user entry = a named OS user + a set of SSH-key
// references + sudo flag + optional password. A ca entry = trust of an external
// CA key restricted to a set of principals.
//
// KeyIDs references ssh_keys rows via the access_entry_keys join table rather
// than copying key material: rotation is swapping the references, and deleting
// a key from the inventory cascades to empty the set (which locks the user
// everywhere the entry applies — "revoke this YubiKey fleet-wide" for free).
type AccessEntry struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	Type      Type

	// Target scope: an entry is scoped to a set of OUs XOR a single node XOR
	// the account default. One user CAN live in several units — OUIDs is a set
	// (join table), and the entry applies to every unit in it. Empty OUIDs with
	// nil NodeID means the entry is not attached to any target: "unassigned",
	// applies to no node — UNLESS AccountWide is set, in which case it is the
	// account-level default (the fleet-wide fallback for nodes the OU chain
	// leaves unresolved, mirroring account_policies). New entries default to
	// unassigned so creating one never silently grants access across the fleet.
	OUIDs  []uuid.UUID
	NodeID *uuid.UUID

	// AccountWide only has meaning when OUIDs is empty and NodeID nil: true =
	// account default (applies fleet-wide), false = unassigned (applies nowhere).
	AccountWide bool

	// User-type fields.
	Username string
	Sudo     bool
	// Password is optional and encrypted at rest. nil means no password: the
	// user logs in by key and sudo (when enabled) runs NOPASSWD, otherwise a
	// key-only user could never sudo at all.
	Password *string
	// AdoptUnmanaged opts in to taking over a pre-existing OS user of the same
	// name. Without it the agent reports skipped_unmanaged and leaves the user
	// alone, so the platform never silently seizes an account it did not create.
	AdoptUnmanaged bool

	// CA-type field: the principals the trusted CA is allowed to certify.
	Principals []string

	// KeyIDs references ssh_keys rows. For a user entry these are the authorized
	// keys; for a ca entry it is the single trusted CA key.
	KeyIDs []uuid.UUID

	CreatedAt time.Time
	UpdatedAt time.Time
}

// AdoptsWithoutCredential reports a user entry that takes over an existing
// account while giving it no way in.
func (e *AccessEntry) AdoptsWithoutCredential() bool {
	return e.Type == TypeUser && e.AdoptUnmanaged && len(e.KeyIDs) == 0 && e.Password == nil
}

// TargetScope reports the entry's scope: "node", "ou", "account" or
// "unassigned".
func (e *AccessEntry) TargetScope() string {
	switch {
	case e.NodeID != nil:
		return "node"
	case len(e.OUIDs) > 0:
		return "ou"
	case e.AccountWide:
		return "account"
	default:
		return "unassigned"
	}
}

// Validate enforces the structural invariants that do not require the entry's
// keys to be hydrated. Key-type conformance (which needs the ssh_keys rows) is
// checked separately with ValidateKeyConformance at the service layer.
func (e *AccessEntry) Validate() error {
	if len(e.OUIDs) > 0 && e.NodeID != nil {
		return ErrInvalidTarget
	}
	if !IsValidType(string(e.Type)) {
		return ErrInvalidType
	}
	switch e.Type {
	case TypeUser:
		u := strings.TrimSpace(e.Username)
		if u == "" {
			return ErrUsernameRequired
		}
		if !usernameRe.MatchString(u) {
			return ErrUsernameInvalid
		}
		if _, reserved := reservedUsernames[u]; reserved {
			return ErrUsernameReserved
		}
	case TypeCA:
		// A CA entry is a pure trust anchor — principals live on USER entries
		// (the OpenSSH AuthorizedPrincipalsFile model), so none are required
		// here. Legacy CA principals are still carried if present.
	}
	return nil
}

// ValidatePrincipals rejects any certificate principal the agent would refuse.
// It is separate from Validate because the service sets Principals after
// construction; call it wherever Principals is assigned so a bad value is a 400
// at save time rather than a config the agent rejects wholesale on arrival.
func (e *AccessEntry) ValidatePrincipals() error {
	for _, p := range e.Principals {
		if !principalRe.MatchString(p) {
			return fmt.Errorf("%w: %q (allowed: letters, digits, and . _ @ -)", ErrPrincipalInvalid, p)
		}
	}
	return nil
}

// ValidateKeyConformance checks that every referenced key exists in the account
// and that its type matches the entry: user entries take "ssh" keys, a ca entry
// takes exactly one "ca" key. keyTypes maps each referenced key id to its type.
func (e *AccessEntry) ValidateKeyConformance(keyTypes map[uuid.UUID]ssh_key.KeyType) error {
	for _, id := range e.KeyIDs {
		kt, ok := keyTypes[id]
		if !ok {
			return ErrUnknownKey
		}
		switch e.Type {
		case TypeUser:
			if kt != ssh_key.KeyTypeSSH {
				return ErrKeyTypeMismatch
			}
		case TypeCA:
			if kt != ssh_key.KeyTypeCA {
				return ErrKeyTypeMismatch
			}
		}
	}
	if e.Type == TypeCA && len(e.KeyIDs) != 1 {
		return ErrCAKeyRequired
	}
	return nil
}

// UserLifecycle is the derived login state of a user access entry.
type UserLifecycle string

const (
	// LifecycleActive means the entry has at least one key: the user can log in.
	LifecycleActive UserLifecycle = "active"
	// LifecycleLocked means the key set is empty: the user is locked (account
	// expired via `chage -E`), but not deleted and its home is kept intact.
	LifecycleLocked UserLifecycle = "locked"
)

// DesiredState is the pure, derived target state of a USER entry. It is the
// single source of truth the agent provisions toward; the lifecycle is computed
// here, never stored or configured. CA entries have no DesiredState.
type DesiredState struct {
	Lifecycle    UserLifecycle
	SudoEnabled  bool
	SudoNoPasswd bool // NOPASSWD when sudo is on but no password is set
	HasPassword  bool
}

// DesiredState derives the target state from the entry's key set and password.
func (e *AccessEntry) DesiredState() DesiredState {
	ds := DesiredState{
		Lifecycle:   LifecycleLocked,
		SudoEnabled: e.Sudo,
		HasPassword: e.Password != nil && *e.Password != "",
	}
	// Active if the user has ANY credential — a key OR a password. Password
	// login is allowed by default (an OU/node policy may disable it, but that is
	// a separate sshd setting, not a reason to expire the account). Locking is
	// reserved for a user with no way in at all: no keys and no password.
	if len(e.KeyIDs) > 0 || ds.HasPassword {
		ds.Lifecycle = LifecycleActive
	}
	if e.Sudo {
		ds.SudoNoPasswd = !ds.HasPassword
	}
	return ds
}

// newAccessEntry builds a validated entry with the target invariant applied.
func newAccessEntry(accountID uuid.UUID, t Type, ouIDs []uuid.UUID, nodeID *uuid.UUID) *AccessEntry {
	now := time.Now()
	return &AccessEntry{
		ID:        uuid.New(),
		AccountID: accountID,
		Type:      t,
		OUIDs:     DedupOUIDs(ouIDs),
		NodeID:    nodeID,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// DedupOUIDs drops duplicate unit ids, preserving order — the join table's PK
// would reject a duplicate anyway, better to normalize than to error.
func DedupOUIDs(in []uuid.UUID) []uuid.UUID {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[uuid.UUID]bool, len(in))
	out := make([]uuid.UUID, 0, len(in))
	for _, id := range in {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// NewUserAccessEntry creates a validated user entry. Keys are attached
// separately (per-key attach/detach), so an entry starts with an empty set —
// which DesiredState reports as locked until a key is attached.
func NewUserAccessEntry(accountID uuid.UUID, username string, sudo bool, password *string, ouIDs []uuid.UUID, nodeID *uuid.UUID) (*AccessEntry, error) {
	e := newAccessEntry(accountID, TypeUser, ouIDs, nodeID)
	e.Username = strings.TrimSpace(username)
	e.Sudo = sudo
	e.Password = password
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}

// NewCAAccessEntry creates a validated ca entry trusting caKeyID. A CA is a
// pure trust anchor — certificate principals live on USER entries — so it
// carries none. Key-type conformance is checked later with ValidateKeyConformance.
func NewCAAccessEntry(accountID uuid.UUID, caKeyID uuid.UUID, ouIDs []uuid.UUID, nodeID *uuid.UUID) (*AccessEntry, error) {
	e := newAccessEntry(accountID, TypeCA, ouIDs, nodeID)
	e.KeyIDs = []uuid.UUID{caKeyID}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}

// nonEmpty trims and drops blank principals, preserving order.
func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
