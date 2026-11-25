package organizational_unit

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// ResolveDeviceProfile walks node → OU → root → account and returns the first
// declared equipage with its source, ok=false when no level declares one.
// One block, not per-field: nearer levels override the whole equipage.
func ResolveDeviceProfile(node *device_profile.Equipage, ou, root *OrganizationalUnit, account *AccountPolicy) (device_profile.Equipage, PolicySource, bool) {
	if node != nil && !node.IsZero() {
		return *node, SourceNode, true
	}
	if ou != nil && ou.DeviceProfile != nil && !ou.DeviceProfile.IsZero() {
		return *ou.DeviceProfile, SourceOU, true
	}
	if root != nil && root.DeviceProfile != nil && !root.DeviceProfile.IsZero() {
		return *root.DeviceProfile, SourceRootOU, true
	}
	if account != nil && account.DeviceProfile != nil && !account.DeviceProfile.IsZero() {
		return *account.DeviceProfile, SourceAccount, true
	}
	return device_profile.Equipage{}, SourceDefault, false
}

// ResolveLEDConfig walks node → OU → root → account and returns the first
// declared LED configuration with its source, ok=false when no level declares
// one. One block like the device profile: nearer levels override the whole
// configuration, because half-merged LED state (this level's stealth with that
// level's sources) is not a state anyone declared.
func ResolveLEDConfig(nodeCfg *node.LEDConfig, ou, root *OrganizationalUnit, account *AccountPolicy) (node.LEDConfig, PolicySource, bool) {
	if nodeCfg != nil && !nodeCfg.IsZero() {
		return *nodeCfg, SourceNode, true
	}
	if ou != nil && ou.LEDConfig != nil && !ou.LEDConfig.IsZero() {
		return *ou.LEDConfig, SourceOU, true
	}
	if root != nil && root.LEDConfig != nil && !root.LEDConfig.IsZero() {
		return *root.LEDConfig, SourceRootOU, true
	}
	if account != nil && account.LEDConfig != nil && !account.LEDConfig.IsZero() {
		return *account.LEDConfig, SourceAccount, true
	}
	return node.LEDConfig{}, SourceDefault, false
}

// ResolveWatchdogConfig walks node → OU → root → account and returns the
// first declared watchdog configuration with its source, ok=false when no
// level declares one. One block like the LED config: half-merged supervision
// (this level's ping targets with that level's timeout) is not a state anyone
// declared. The guard is nil, never IsZero: a node's own DISABLED config is a
// real declaration that shields it from inheritance, while the OU and account
// levels store only armed configs by the update commands' contract (a
// disabled submission clears the declaration). Resolution says what is
// DESIRED; nothing arms a device without a human present, so a device
// receives this only through its per-node apply.
func ResolveWatchdogConfig(nodeCfg *node.WatchdogConfig, ou, root *OrganizationalUnit, account *AccountPolicy) (node.WatchdogConfig, PolicySource, bool) {
	if nodeCfg != nil {
		return *nodeCfg, SourceNode, true
	}
	if ou != nil && ou.WatchdogConfig != nil {
		return *ou.WatchdogConfig, SourceOU, true
	}
	if root != nil && root.WatchdogConfig != nil {
		return *root.WatchdogConfig, SourceRootOU, true
	}
	if account != nil && account.WatchdogConfig != nil {
		return *account.WatchdogConfig, SourceAccount, true
	}
	return node.WatchdogConfig{}, SourceDefault, false
}

// Domain validation errors.
var (
	ErrNameRequired    = errors.New("organizational unit name is required")
	ErrCircularParent  = errors.New("organizational unit cannot be its own parent")
	ErrNestingTooDeep  = errors.New("organizational units support only two levels (root and one nested level)")
	ErrParentIsNotRoot = errors.New("a nested organizational unit's parent must be a root unit")
)

// OrganizationalUnit groups nodes and carries inheritable policy defaults.
// The hierarchy is two levels: a root unit (ParentID == nil) and units nested
// one level under a root.
type OrganizationalUnit struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	Name      string
	ParentID  *uuid.UUID // nil for a root unit
	// Position orders siblings (units sharing the same parent, or roots) in the
	// tree. Lower comes first; ties break by CreatedAt.
	Position int

	// Inheritable policies. A nil value means "inherit from the parent unit
	// (and ultimately the system default)". Extend this set as new policies are
	// added — nil always means inherit.
	NetworkFirst *bool // boot the network bootstrap first (server-controlled fleet)
	BootToOS     *bool // in bootstrap, hand off to the installed OS on NVMe
	// SSHPasswordAuth controls whether sshd accepts password logins. Named in
	// the positive (not disable_password_auth) so "off" unambiguously means
	// passwords are refused; systemDefault is true, preserving the agent's
	// historical behaviour when no level sets it.
	SSHPasswordAuth *bool

	// Inheritable provisioning defaults (nil = inherit).
	DefaultUsername *string    // default OS username for the group
	DefaultPassword *string    // default OS password for the group (ENCRYPTED at rest)
	SSHKeyID        *uuid.UUID // default SSH key for the group
	OSImageID       *uuid.UUID // default OS image for the group
	// DefaultPasswordUnreadable: a password is stored but no configured key
	// decrypts it. DefaultPassword is nil in that case; see node.Node for the
	// full contract. Never persisted.
	DefaultPasswordUnreadable bool

	// DeviceProfile is an inheritable hardware equipage for the unit's nodes
	// (nil = inherit). Inherited as ONE block — profile, variant and modules
	// together — never field-by-field: half a hardware declaration is not a
	// meaningful state.
	DeviceProfile *device_profile.Equipage

	// LEDConfig is the inheritable LED behaviour for the unit's nodes
	// (nil = inherit). One block, like the device profile; a node's own LED
	// config overrides it entirely. Declaring it here STORES the default — a
	// device receives it through its LED apply, not on this save.
	LEDConfig *node.LEDConfig

	// WatchdogConfig is the inheritable watchdog supervision for the unit's
	// nodes (nil = inherit). One block like the LED config, and with the same
	// no-dispatch rule — plus the watchdog's own stricter doctrine: NOTHING
	// may arm a device without a human present, so this save never converges
	// anything. A device gets watchdog state only through its per-node apply.
	WatchdogConfig *node.WatchdogConfig

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewOrganizationalUnit creates a unit. A nil parentID makes it a root unit.
func NewOrganizationalUnit(accountID uuid.UUID, name string, parentID *uuid.UUID) (*OrganizationalUnit, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrNameRequired
	}
	now := time.Now()
	return &OrganizationalUnit{
		ID:        uuid.New(),
		AccountID: accountID,
		Name:      name,
		ParentID:  parentID,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// IsRoot reports whether this is a root (top-level) unit.
func (o *OrganizationalUnit) IsRoot() bool {
	return o.ParentID == nil
}

// PolicyFlag is the API/tri-state representation of an inheritable boolean
// policy: "inherit" (nil), "on" (true), or "off" (false). It disambiguates
// "inherit from parent" from a concrete value, which a bare *bool cannot.
type PolicyFlag string

const (
	PolicyInherit PolicyFlag = "inherit"
	PolicyOn      PolicyFlag = "on"
	PolicyOff     PolicyFlag = "off"
)

// ToBool converts a flag to the stored representation (nil == inherit). An
// empty/unknown flag is treated as inherit.
func (p PolicyFlag) ToBool() *bool {
	switch p {
	case PolicyOn:
		v := true
		return &v
	case PolicyOff:
		v := false
		return &v
	default:
		return nil
	}
}

// Validate refuses a value outside the flag's grammar. Empty is accepted and
// means inherit, as ToBool reads it; anything else that is not inherit, on or
// off is refused, because ToBool would silently read it as inherit and the
// request would answer success while storing something the caller never
// asked for. field names the request field in the message.
func (p PolicyFlag) Validate(field string) error {
	switch p {
	case "", PolicyInherit, PolicyOn, PolicyOff:
		return nil
	default:
		return fmt.Errorf("%s must be one of inherit, on, off", field)
	}
}

// PolicyFlagFromBool is the inverse of ToBool.
func PolicyFlagFromBool(b *bool) PolicyFlag {
	if b == nil {
		return PolicyInherit
	}
	if *b {
		return PolicyOn
	}
	return PolicyOff
}

// PolicySource identifies which level supplied an effective policy value.
type PolicySource string

const (
	SourceNode    PolicySource = "node"
	SourceOU      PolicySource = "ou"
	SourceRootOU  PolicySource = "root_ou"
	SourceAccount PolicySource = "account"
	SourceDefault PolicySource = "default"
)

// AccountPolicy is the account-wide policy fallback: it applies to every node
// the OU chain leaves unresolved — most importantly unassigned nodes, which
// have no OU at all. Field semantics mirror OrganizationalUnit (nil = unset,
// fall through to the built-in default).
type AccountPolicy struct {
	AccountID uuid.UUID

	NetworkFirst    *bool
	BootToOS        *bool
	SSHPasswordAuth *bool

	DefaultUsername *string
	DefaultPassword *string // ENCRYPTED at rest
	SSHKeyID        *uuid.UUID
	OSImageID       *uuid.UUID
	// DefaultPasswordUnreadable: stored but undecryptable; see node.Node.
	// Never persisted.
	DefaultPasswordUnreadable bool

	// DeviceProfile: account-wide equipage fallback (nil = none). Same
	// one-block inheritance as the OU field.
	DeviceProfile *device_profile.Equipage

	// LEDConfig: account-wide LED fallback (nil = none). Same one-block
	// inheritance as the OU field.
	LEDConfig *node.LEDConfig

	// WatchdogConfig: account-wide watchdog fallback (nil = none). Same
	// one-block inheritance and the same no-dispatch rule as the OU field.
	WatchdogConfig *node.WatchdogConfig

	CreatedAt time.Time
	UpdatedAt time.Time
}

// ResolvedFlag is an effective boolean policy plus where it came from.
type ResolvedFlag struct {
	Value  bool         `json:"value"`
	Source PolicySource `json:"source"`
}

// ResolvedString is an effective string value plus its source. Present reports
// whether any level supplied a value.
type ResolvedString struct {
	Value   string       `json:"value"`
	Present bool         `json:"present"`
	Source  PolicySource `json:"source"`
}

// ResolvedUUID is an effective UUID value plus its source.
type ResolvedUUID struct {
	Value   *uuid.UUID   `json:"value"`
	Present bool         `json:"present"`
	Source  PolicySource `json:"source"`
}

// EffectivePolicy is the fully resolved policy set for a node.
type EffectivePolicy struct {
	NetworkFirst    ResolvedFlag `json:"network_first"`
	BootToOS        ResolvedFlag `json:"boot_to_os"`
	SSHPasswordAuth ResolvedFlag `json:"ssh_password_auth"`

	DefaultUsername ResolvedString `json:"default_username"`
	// Password value is never exposed; only whether one is set and its source.
	DefaultPassword ResolvedString `json:"default_password"`
	SSHKeyID        ResolvedUUID   `json:"ssh_key_id"`
	OSImageID       ResolvedUUID   `json:"os_image_id"`
}

// ResolveString walks node ("" = inherit) -> ou -> root -> account -> "" (absent).
//
// Exported because the command-building path resolves the same chain and must
// report the same source: a provisioning refusal that names the wrong level
// sends the operator to edit a unit that carries nothing, and a second copy of
// this walk is how the API view and the dispatch view start disagreeing about
// where a value came from.
func ResolveString(node string, ou, root, account *string) ResolvedString {
	if node != "" {
		return ResolvedString{Value: node, Present: true, Source: SourceNode}
	}
	if ou != nil && *ou != "" {
		return ResolvedString{Value: *ou, Present: true, Source: SourceOU}
	}
	if root != nil && *root != "" {
		return ResolvedString{Value: *root, Present: true, Source: SourceRootOU}
	}
	if account != nil && *account != "" {
		return ResolvedString{Value: *account, Present: true, Source: SourceAccount}
	}
	return ResolvedString{Source: SourceDefault}
}

// resolveUUID walks node -> ou -> root -> account -> nil (absent).
func resolveUUID(node, ou, root, account *uuid.UUID) ResolvedUUID {
	if node != nil {
		return ResolvedUUID{Value: node, Present: true, Source: SourceNode}
	}
	if ou != nil {
		return ResolvedUUID{Value: ou, Present: true, Source: SourceOU}
	}
	if root != nil {
		return ResolvedUUID{Value: root, Present: true, Source: SourceRootOU}
	}
	if account != nil {
		return ResolvedUUID{Value: account, Present: true, Source: SourceAccount}
	}
	return ResolvedUUID{Source: SourceDefault}
}

// maskedPasswordSource resolves only the SOURCE of the password (never its value)
// walking node -> ou -> root -> account.
func maskedPassword(node string, ou, root, account *string) ResolvedString {
	r := ResolveString(node, ou, root, account)
	r.Value = "" // never expose the password value
	return r
}

// resolveFlag returns the first non-nil value walking node -> ou -> root ->
// account, and falls back to systemDefault. The corresponding source is
// reported.
func resolveFlag(node, ou, root, account *bool, systemDefault bool) ResolvedFlag {
	if node != nil {
		return ResolvedFlag{Value: *node, Source: SourceNode}
	}
	if ou != nil {
		return ResolvedFlag{Value: *ou, Source: SourceOU}
	}
	if root != nil {
		return ResolvedFlag{Value: *root, Source: SourceRootOU}
	}
	if account != nil {
		return ResolvedFlag{Value: *account, Source: SourceAccount}
	}
	return ResolvedFlag{Value: systemDefault, Source: SourceDefault}
}

// NodePolicyInputs are a node's own policy fields fed to the resolver.
type NodePolicyInputs struct {
	NetworkFirst    *bool
	BootToOS        *bool
	SSHPasswordAuth *bool
	DefaultUsername string
	DefaultPassword string
	SSHKeyID        *uuid.UUID
	OSImageID       *uuid.UUID
}

// ResolveEffectivePolicy computes the effective policy for a node given its own
// overrides, its (optional) OU, the (optional) root OU, and the (optional)
// account-wide policy. Any of ou/root/account may be nil (unassigned node, a
// root-level OU with no parent, or an account that never set a default policy).
// Boolean defaults are false; string/uuid defaults are absent.
func ResolveEffectivePolicy(node NodePolicyInputs, ou, root *OrganizationalUnit, account *AccountPolicy) EffectivePolicy {
	var (
		ouNet, ouBoot, rootNet, rootBoot, accNet, accBoot    *bool
		ouPwAuth, rootPwAuth, accPwAuth                      *bool
		ouUser, ouPass, rootUser, rootPass, accUser, accPass *string
		ouSSH, ouImage, rootSSH, rootImage, accSSH, accImage *uuid.UUID
	)
	if ou != nil {
		ouNet, ouBoot = ou.NetworkFirst, ou.BootToOS
		ouPwAuth = ou.SSHPasswordAuth
		ouUser, ouPass = ou.DefaultUsername, ou.DefaultPassword
		ouSSH, ouImage = ou.SSHKeyID, ou.OSImageID
	}
	if root != nil {
		rootNet, rootBoot = root.NetworkFirst, root.BootToOS
		rootPwAuth = root.SSHPasswordAuth
		rootUser, rootPass = root.DefaultUsername, root.DefaultPassword
		rootSSH, rootImage = root.SSHKeyID, root.OSImageID
	}
	if account != nil {
		accNet, accBoot = account.NetworkFirst, account.BootToOS
		accPwAuth = account.SSHPasswordAuth
		accUser, accPass = account.DefaultUsername, account.DefaultPassword
		accSSH, accImage = account.SSHKeyID, account.OSImageID
	}
	return EffectivePolicy{
		// systemDefault TRUE: a node nobody has configured should provision its
		// EEPROM for network boot when it is approved. That is the product's
		// whole onboarding story, and leaving it off meant every new user
		// adopted a device, approved it, and got silence — the reason was a log
		// line in the backend that no operator ever sees.
		//
		// The blast radius is smaller than it looks: the auto-provision checks
		// "is this node the recovery system booted from a provisioning card"
		// BEFORE it reads this policy (eeprom_provision.MaybeProvisionNetworkFirst),
		// so it only ever fires at onboarding from a provisioning card and never
		// touches a node running its own operating system, whatever it boots
		// from.
		NetworkFirst: resolveFlag(node.NetworkFirst, ouNet, rootNet, accNet, true),
		BootToOS:     resolveFlag(node.BootToOS, ouBoot, rootBoot, accBoot, false),
		// systemDefault true: absent everywhere, password auth stays on — the
		// agent's historical behaviour. Turning it off is always an explicit act.
		SSHPasswordAuth: resolveFlag(node.SSHPasswordAuth, ouPwAuth, rootPwAuth, accPwAuth, true),
		DefaultUsername: ResolveString(node.DefaultUsername, ouUser, rootUser, accUser),
		DefaultPassword: maskedPassword(node.DefaultPassword, ouPass, rootPass, accPass),
		SSHKeyID:        resolveUUID(node.SSHKeyID, ouSSH, rootSSH, accSSH),
		OSImageID:       resolveUUID(node.OSImageID, ouImage, rootImage, accImage),
	}
}
