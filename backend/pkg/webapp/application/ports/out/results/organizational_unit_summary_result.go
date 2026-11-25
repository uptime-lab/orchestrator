package results

import (
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// OrganizationalUnitSummaryResult is the Organization page's one-call overview
// of a unit. Sections the caller may not read are ABSENT and named in Omitted
// (the account-export pattern: an empty list and an unreadable one must not be
// the same JSON). Each section is a first-N sample plus the true total; every
// row carries the OU-perspective columns (provenance, inheritance, effect) the
// original list pages cannot show — that is the section's licence to exist.
type OrganizationalUnitSummaryResult struct {
	Unit OrganizationalUnitResult `json:"unit"`
	// EffectivePolicy resolves the unit's chain (unit -> root -> account ->
	// system default) with the source of every value. The node level is absent
	// by construction; a password is reported as presence + source only.
	EffectivePolicy organizational_unit.EffectivePolicy `json:"effective_policy"`
	// EffectiveDeviceProfile mirrors the policy resolution for the hardware
	// equipage block; absent when no level sets one.
	EffectiveDeviceProfile       *device_profile.Equipage         `json:"effective_device_profile,omitempty"`
	EffectiveDeviceProfileSource organizational_unit.PolicySource `json:"effective_device_profile_source,omitempty"`
	// Best-effort display names for the resolved provisioning references.
	EffectiveSSHKeyName  string `json:"effective_ssh_key_name,omitempty"`
	EffectiveOSImageName string `json:"effective_os_image_name,omitempty"`

	Counts OUSummaryCounts `json:"counts"`
	// PasswordAuthImpact is how many nodes an ssh_password_auth flip on THIS
	// unit would push apply_access_config to — shown before the operator saves,
	// not discovered after.
	PasswordAuthImpact int `json:"password_auth_impact"`

	Nodes         *OUSummaryNodesSection         `json:"nodes,omitempty"`
	AccessEntries *OUSummaryAccessSection        `json:"access_entries,omitempty"`
	Secrets       *OUSummarySecretsSection       `json:"secrets,omitempty"`
	Workflows     *OUSummaryWorkflowsSection     `json:"workflows,omitempty"`
	MasterTokens  *OUSummaryMasterTokensSection  `json:"master_tokens,omitempty"`
	// Omitted names the sections the caller's role could not read. Utilities
	// are deliberately not part of the summary — the page reads the existing
	// utilities endpoints, which already carry per-OU facets.
	Omitted []string `json:"omitted,omitempty"`
}

// OUSummaryCounts are the true totals behind the sampled sections.
type OUSummaryCounts struct {
	Children      int `json:"children"`
	Nodes         int `json:"nodes"`
	ChildNodes    int `json:"child_nodes"`
	AccessEntries int `json:"access_entries"`
	Secrets       int `json:"secrets"`
	Workflows     int `json:"workflows"`
	MasterTokens  int `json:"master_tokens"`
}

// OUSummaryNodesSection samples the devices this unit's policies reach.
type OUSummaryNodesSection struct {
	Total int                   `json:"total"`
	Items []OUSummaryNodeResult `json:"items"`
}

// OUSummaryNodeResult is one device with its policy-inheritance status —
// whether this unit's policies actually decide anything on it.
type OUSummaryNodeResult struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Status string    `json:"status"`
	// ViaChildOU is set when the node is a member of a child unit rather than
	// of this unit directly (root-unit view).
	ViaChildOU *uuid.UUID `json:"via_child_ou,omitempty"`
	// PolicyMode aggregates the per-field statuses: "inherits" (every field
	// flows from this unit's chain), "overridden" (the node sets its own
	// values — OverriddenFields names them), or "via_child" (a child unit
	// answers between this root and the node).
	PolicyMode       string   `json:"policy_mode"`
	OverriddenFields []string `json:"overridden_fields,omitempty"`
}

// OUSummaryAccessSection samples the shell-access entries reaching this unit.
type OUSummaryAccessSection struct {
	Total int                          `json:"total"`
	Items []OUSummaryAccessEntryResult `json:"items"`
}

// OUSummaryAccessEntryResult is one access entry with where it takes effect
// from — the unit itself, its parent, or the account-wide default.
type OUSummaryAccessEntryResult struct {
	ID       uuid.UUID `json:"id"`
	Type     string    `json:"type"` // user | ca
	Username string    `json:"username,omitempty"`
	KeyCount int       `json:"key_count"`
	Sudo     bool      `json:"sudo"`
	// EffectiveFrom: unit | parent | account.
	EffectiveFrom string `json:"effective_from"`
	// Shadowed: a nearer level defines the same OS username, which overrides
	// this entry ENTIRELY on this unit's nodes (the resolution rule).
	Shadowed bool `json:"shadowed"`
}

// OUSummarySecretsSection samples the unit's effective secret set.
type OUSummarySecretsSection struct {
	Total int                     `json:"total"`
	Items []OUSummarySecretResult `json:"items"`
}

// OUSummarySecretResult is one delivered secret with its provenance. Metadata
// only — never the value; names alone are already gated (secret.read).
type OUSummarySecretResult struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Sensitive bool      `json:"sensitive"`
	// SourceLevel: ou | root. Via: direct | tag (Tag set when via tag).
	SourceLevel string `json:"source_level"`
	Via         string `json:"via"`
	Tag         string `json:"tag,omitempty"`
}

// OUSummaryWorkflowsSection samples the playbooks scoped to this unit.
type OUSummaryWorkflowsSection struct {
	Total int                       `json:"total"`
	Items []OUSummaryWorkflowResult `json:"items"`
}

// OUSummaryWorkflowResult is one playbook with the width of its scope.
type OUSummaryWorkflowResult struct {
	ID     uuid.UUID `json:"id"`
	Name   string    `json:"name"`
	Status string    `json:"status"`
	// ScopeOUCount is how many units the playbook's scope names; 1 means it is
	// exclusive to this unit.
	ScopeOUCount int `json:"scope_ou_count"`
}

// OUSummaryMasterTokensSection samples adoption tokens targeting this unit.
type OUSummaryMasterTokensSection struct {
	Total int                          `json:"total"`
	Items []OUSummaryMasterTokenResult `json:"items"`
}

// OUSummaryMasterTokenResult is one adoption token (name and id only).
type OUSummaryMasterTokenResult struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}
