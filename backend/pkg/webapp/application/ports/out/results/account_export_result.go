package results

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_template"
)

// AccountExportResult is everything an account owns, in one portable document.
//
// It exists so a tenant can leave with their configuration. Read the Notice
// field before reading anything else: this document
// carries configuration and metadata, and deliberately carries no credential
// material at all.
//
// The `has_*` booleans are the shape that keeps that promise honest. "There is
// a default password on this OU" is information the customer needs when
// rebuilding — it tells them a credential must be re-entered here — while the
// password itself is exactly what must not travel. Dropping the field entirely
// would leave a silent gap; exporting the value would put a fleet credential in
// a file that no key rotation can ever reach.
type AccountExportResult struct {
	// FormatVersion is bumped when a field is removed or changes meaning, never
	// for additions — see account_export.FormatVersion.
	FormatVersion int    `json:"format_version"`
	GeneratedAt   string `json:"generated_at"`
	Notice        string `json:"notice"`

	Account             AccountExportAccount       `json:"account"`
	AccountPolicy       *AccountExportPolicy       `json:"account_policy,omitempty"`
	Members             []AccountExportMember      `json:"members,omitempty"`
	OrganizationalUnits []AccountExportOU          `json:"organizational_units,omitempty"`
	Nodes               []AccountExportNode        `json:"nodes,omitempty"`
	AccessEntries       []AccountExportAccessEntry `json:"access_entries,omitempty"`
	SSHKeys             []AccountExportSSHKey      `json:"ssh_keys,omitempty"`
	Secrets             []AccountExportSecret      `json:"secrets,omitempty"`
	Playbooks           []AccountExportPlaybook    `json:"playbooks,omitempty"`
	Images              []AccountExportImage       `json:"images,omitempty"`
	Utilities           []AccountExportUtilityAssignment `json:"utilities,omitempty"`

	// Omitted names sections that could not be read. A partial export that says
	// so beats a 500 for someone who is leaving, but it must never look
	// complete: an empty `nodes` array and an unreadable one are the same JSON
	// without this.
	Omitted []AccountExportOmission `json:"omitted,omitempty"`
}

// AccountExportOmission names a section that failed, without naming rows. The
// reason is deliberately coarse — the detail is in the server log, where it
// belongs, and this document is written to be shared.
type AccountExportOmission struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
}

type AccountExportAccount struct {
	ID               uuid.UUID `json:"id"`
	Name             string    `json:"name"`
	RequireTwoFactor bool      `json:"require_two_factor"`
	CreatedAt        string    `json:"created_at"`
}

// AccountExportPolicy renders the inheritable flags as the same tri-state the
// API uses (inherit|on|off) rather than as a JSON null, because "inherit" and
// "absent" are different states and a null loses the difference.
type AccountExportPolicy struct {
	NetworkFirst       string  `json:"network_first,omitempty"`
	BootToOS           string  `json:"boot_to_os,omitempty"`
	SSHPasswordAuth    string  `json:"ssh_password_auth,omitempty"`
	DefaultUsername    string  `json:"default_username,omitempty"`
	HasDefaultPassword bool    `json:"has_default_password"`
	SSHKeyID           *string `json:"ssh_key_id,omitempty"`
	OSImageID          *string `json:"os_image_id,omitempty"`
}

type AccountExportBinding struct {
	Role                 string  `json:"role"`
	OrganizationalUnitID *string `json:"organizational_unit_id,omitempty"`
}

type AccountExportMember struct {
	UserID   uuid.UUID              `json:"user_id"`
	Email    string                 `json:"email"`
	Name     string                 `json:"name,omitempty"`
	Status   string                 `json:"status"`
	Bindings []AccountExportBinding `json:"bindings,omitempty"`
}

type AccountExportOU struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	ParentID           *string   `json:"parent_id,omitempty"`
	Position           int       `json:"position"`
	NetworkFirst       string    `json:"network_first,omitempty"`
	BootToOS           string    `json:"boot_to_os,omitempty"`
	SSHPasswordAuth    string    `json:"ssh_password_auth,omitempty"`
	DefaultUsername    string    `json:"default_username,omitempty"`
	HasDefaultPassword bool      `json:"has_default_password"`
	SSHKeyID           *string   `json:"ssh_key_id,omitempty"`
	OSImageID          *string   `json:"os_image_id,omitempty"`
}

// AccountExportNode carries no unique_token. That value is the device's
// credential for this account: a node record without it is a description of
// hardware, and with it is a way onto the platform.
type AccountExportNode struct {
	ID                   uuid.UUID `json:"id"`
	Name                 string    `json:"name,omitempty"`
	Hostname             string    `json:"hostname,omitempty"`
	SerialNumber         string    `json:"serial_number,omitempty"`
	Status               string    `json:"status,omitempty"`
	AgentVersion         string    `json:"agent_version,omitempty"`
	AgentKind            string    `json:"agent_kind,omitempty"`
	IPv4                 string    `json:"ipv4,omitempty"`
	MACAddress           string    `json:"mac_address,omitempty"`
	SoC                  string    `json:"soc,omitempty"`
	Notes                string    `json:"notes,omitempty"`
	NetworkFirst         string    `json:"network_first,omitempty"`
	BootToOS             string    `json:"boot_to_os,omitempty"`
	OrganizationalUnitID *string   `json:"organizational_unit_id,omitempty"`
}

type AccountExportAccessEntry struct {
	ID                    uuid.UUID `json:"id"`
	Kind                  string    `json:"kind"`
	Username              string    `json:"username,omitempty"`
	Sudo                  bool      `json:"sudo"`
	HasPassword           bool      `json:"has_password"`
	Principals            []string  `json:"certificate_principals,omitempty"`
	SSHKeyIDs             []string  `json:"ssh_key_ids,omitempty"`
	AccountWide           bool      `json:"account_wide"`
	OrganizationalUnitIDs []string  `json:"organizational_unit_ids,omitempty"`
	NodeID                *string   `json:"node_id,omitempty"`
}

// AccountExportSSHKey carries the public half only. HasPrivateHalf tells a
// customer that the platform generated this key and holds the private side, so
// they know to generate a fresh one at the destination rather than waiting for
// a private key that is never coming.
type AccountExportSSHKey struct {
	ID    uuid.UUID `json:"id"`
	Alias string    `json:"alias"`
	// KeyType is "ssh" (an authorized_keys entry) or "ca" (a trust anchor). The
	// distinction matters when rebuilding: a CA is accepted, never issued here.
	KeyType     string   `json:"key_type,omitempty"`
	PublicKey   string   `json:"public_key,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	IsDefault   bool     `json:"is_default"`
	Tags        []string `json:"tags,omitempty"`
	// HasPrivateHalf means the platform generated this key and holds the private
	// side, which is NOT in this document. It is here so a customer knows to
	// generate a fresh key at the destination rather than wait for one that is
	// never coming.
	HasPrivateHalf bool `json:"has_private_half"`
}

type AccountExportSecretAttachment struct {
	Tag                  string  `json:"tag,omitempty"`
	OrganizationalUnitID *string `json:"organizational_unit_id,omitempty"`
	NodeID               *string `json:"node_id,omitempty"`
}

// AccountExportSecret carries a value ONLY for a non-sensitive env variable —
// the same split the live API applies (`value` vs `has_value`), not a new
// policy. The name is always present because it is the `{{secrets.<name>}}`
// identifier the exported playbooks reference; without it the playbooks are
// unreadable, and with it nothing is disclosed.
type AccountExportSecret struct {
	ID          uuid.UUID                       `json:"id"`
	Name        string                          `json:"name"`
	Key         string                          `json:"key,omitempty"`
	Kind        string                          `json:"kind"`
	Sensitive   bool                            `json:"sensitive"`
	Tags        []string                        `json:"tags,omitempty"`
	Value       string                          `json:"value,omitempty"`
	HasValue    bool                            `json:"has_value"`
	Attachments []AccountExportSecretAttachment `json:"attachments,omitempty"`
}

type AccountExportPlaybook struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status,omitempty"`
	IsShortcut  bool      `json:"is_shortcut"`
	// OrganizationalUnitID is the legacy single-unit field, present only for a
	// playbook scoped to exactly one unit; OrganizationalUnitIDs is the scope.
	OrganizationalUnitID  *string                          `json:"organizational_unit_id,omitempty"`
	OrganizationalUnitIDs []string                         `json:"organizational_unit_ids,omitempty"`
	Graph                 *workflow_template.WorkflowGraph `json:"graph,omitempty"`
}

// AccountExportImage is the inventory, not the bytes. DownloadPath is spelled
// out because a reader of this file no longer has the API documentation, and
// "the images are missing" is the wrong conclusion to reach from their absence.
// AccountExportUtilityAssignment is one utility assignment (one utility × one
// target × one config). Config exports verbatim: by doctrine it carries
// variable/attribute NAMES, never secret material.
type AccountExportUtilityAssignment struct {
	ID                    uuid.UUID `json:"id"`
	UtilityID             string    `json:"utility_id"`
	Scope                 string    `json:"scope"`
	OrganizationalUnitIDs []string  `json:"organizational_unit_ids,omitempty"`
	OrganizationalUnitID *string                `json:"organizational_unit_id,omitempty"`
	NodeID               *string                `json:"node_id,omitempty"`
	AccountWide          bool                   `json:"account_wide"`
	Config               map[string]interface{} `json:"config,omitempty"`
	Enabled              bool                   `json:"enabled"`
}

type AccountExportImage struct {
	ID           uuid.UUID `json:"id"`
	Title        string    `json:"title"`
	Version      string    `json:"version,omitempty"`
	Description  string    `json:"description,omitempty"`
	Kind         string    `json:"kind,omitempty"`
	FileName     string    `json:"file_name,omitempty"`
	SizeBytes    int64     `json:"size_bytes,omitempty"`
	Checksum     string    `json:"checksum,omitempty"`
	DownloadPath string    `json:"download_path"`
}
