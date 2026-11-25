package results

import (
	"time"
	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
)

// NodeResult represents a node in API responses
type NodeResult struct {
	ID              uuid.UUID                      `json:"id"`
	AccountID       uuid.UUID                      `json:"account_id"`
	Name            string                         `json:"name"`
	Hostname        *string                        `json:"hostname"`
	IPv4            *string                        `json:"ipv4"`
	MacAddress      *string                        `json:"mac_address"`
	Fingerprint     *node.HardwareFingerprint      `json:"fingerprint"`
	Status          string                         `json:"status"`
	// PinnedBySlug names the command that imposed a transient status, when one
	// did. It answers the question "restarting" cannot: the platform pins a node
	// at dispatch for any command that MIGHT take the device down, and for a
	// bootloader flash that usually does not happen, so a client shows the
	// alarming word only when the slug is a command that really restarts.
	PinnedBySlug    string                         `json:"pinned_by_slug,omitempty"`
	StorageDevices  map[string]*node.StorageDevice `json:"storage_devices,omitempty"`
	SystemInfo      *node.SystemInfo               `json:"system_info,omitempty"`
	BootOrder       []string                       `json:"boot_order,omitempty"`   // Array of boot device enums
	SSHKeyID        *uuid.UUID                     `json:"ssh_key_id,omitempty"`   // SSH key ID (never password)
	DefaultUsername string                         `json:"default_username"`       // Always include, even if empty
	Notes           string                         `json:"notes"`                  // Free-form operator notes; always include, even if empty
	AgentVersion    string                         `json:"agent_version"`          // Agent software version last reported on connect
	AgentKind       string                         `json:"agent_kind,omitempty"`   // ""/"full" = managing agent; "watcher" = telemetry-only (no commands)
	LastSeenAt      *int64                         `json:"last_seen_at,omitempty"` // Unix timestamp (seconds since epoch) of last activity
	// LastBootAt is when the node last booted, on the SERVER's clock, derived
	// from the agent's uptime rather than the device's wall clock (which a
	// blank hardware RTC puts decades in the past). It rides the node_updated
	// push, so a client can tell that a node has REBOOTED — not merely
	// reconnected — and refresh what depends on that.
	LastBootAt *int64 `json:"last_boot_at,omitempty"` // Unix timestamp (seconds since epoch)

	// Bootloader (EEPROM firmware) identity from the ROM's last HTTP-boot request
	BootloaderVersion   string `json:"bootloader_version,omitempty"`   // short firmware build hash
	BootloaderTimestamp *int64 `json:"bootloader_timestamp,omitempty"` // firmware build time (unix seconds)
	// BootloaderFlashedAt is when the most recent successful EEPROM flash
	// settled (unix seconds, server clock); BootloaderRebootPending says
	// whether that firmware still awaits the boot that activates it. Both
	// absent = unknown, and clients fail open (keep offering the update).
	BootloaderFlashedAt     *int64 `json:"bootloader_flashed_at,omitempty"`
	BootloaderRebootPending *bool  `json:"bootloader_reboot_pending,omitempty"`

	// Organizational unit membership + per-node policy overrides (inherit|on|off)
	OUID            *uuid.UUID                     `json:"ou_id,omitempty"`
	NetworkFirst    organizational_unit.PolicyFlag `json:"network_first"`
	BootToOS        organizational_unit.PolicyFlag `json:"boot_to_os"`
	SSHPasswordAuth organizational_unit.PolicyFlag `json:"ssh_password_auth"`
	OSImageID       *uuid.UUID                     `json:"os_image_id,omitempty"`

	// RecoveryRevertPending marks a one-shot reboot-to-recovery in flight:
	// the node was told to netboot once and its previous boot target will be
	// restored when it reconnects from the bootstrap.
	RecoveryRevertPending bool `json:"recovery_revert_pending,omitempty"`
	// AwaitingReturnAfter names the slug the platform expects the node back
	// from (install_os, reboot, …). Meaningful to a reader mainly while the
	// node is OFFLINE: that combination is "never came back after <slug>".
	AwaitingReturnAfter string     `json:"awaiting_return_after,omitempty"`
	AwaitingReturnSince *time.Time `json:"awaiting_return_since,omitempty"`

	// Boot-loop failsafe: BootFailsafeAt is set when the failsafe last served
	// recovery (drives the "auto-recovered" badge until cleared by a healthy OS
	// boot or an operator acknowledge); BootAttemptCount is the current run of
	// withheld netboot attempts; BootFailsafeThreshold is the per-node override
	// (nil = use the global default).
	BootFailsafeAt        *int64 `json:"boot_failsafe_at,omitempty"` // unix seconds
	BootAttemptCount      int    `json:"boot_attempt_count,omitempty"`
	BootFailsafeThreshold *int   `json:"boot_failsafe_threshold,omitempty"`

	// Watchdog: WatchdogDisarmed is set when the agent's on-device breaker took
	// the watchdog down on its own (drives the node pill until the next
	// confirmed apply clears it); WatchdogDisarmReason carries the breaker's
	// reason string.
	WatchdogDisarmed     bool   `json:"watchdog_disarmed,omitempty"`
	WatchdogDisarmReason string `json:"watchdog_disarm_reason,omitempty"`

	// Master token authentication fields
	ApprovalStatus string     `json:"approval_status"`           // approved, pending_approval, rejected
	MasterTokenID  *uuid.UUID `json:"master_token_id,omitempty"` // Master token used for registration (presence indicates master token auth)

	// AwaitingFirstConnect marks a WebUSB-flashed node that is approved and just
	// waiting to reboot, netboot and connect for the first time.
	AwaitingFirstConnect bool `json:"awaiting_first_connect,omitempty"`

	// DefaultPassword is intentionally NOT included - never returned in API responses
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewNodeResult creates a new NodeResult from a domain Node
func NewNodeResult(domainNode *node.Node) NodeResult {
	// Handle nullable fields - set to nil if empty
	var hostname *string
	if domainNode.Hostname != "" {
		hostname = &domainNode.Hostname
	}

	var ipv4 *string
	if domainNode.IPv4 != "" {
		ipv4 = &domainNode.IPv4
	}

	var macAddress *string
	if domainNode.MacAddress != "" {
		macAddress = &domainNode.MacAddress
	}

	result := NodeResult{
		ID:                    domainNode.ID,
		AccountID:             domainNode.AccountID,
		Name:                  domainNode.Name,
		Hostname:              hostname,
		IPv4:                  ipv4,
		MacAddress:            macAddress,
		Fingerprint:           domainNode.Fingerprint, // Will be nil if not set
		Status:                string(domainNode.Status),
		PinnedBySlug:          domainNode.PinnedBySlug,
		SSHKeyID:              domainNode.SSHKeyID,
		DefaultUsername:       domainNode.DefaultUsername,
		Notes:                 domainNode.Notes,
		AgentVersion:          domainNode.AgentVersion,
		AgentKind:             domainNode.AgentKind,
		OUID:                  domainNode.OUID,
		NetworkFirst:          organizational_unit.PolicyFlagFromBool(domainNode.NetworkFirst),
		BootToOS:              organizational_unit.PolicyFlagFromBool(domainNode.BootToOS),
		SSHPasswordAuth:       organizational_unit.PolicyFlagFromBool(domainNode.SSHPasswordAuth),
		OSImageID:             domainNode.OSImageID,
		RecoveryRevertPending: domainNode.RecoveryRevertPending,
		AwaitingReturnAfter:   domainNode.AwaitingReturnAfter,
		AwaitingReturnSince:   domainNode.AwaitingReturnSince,
		BootAttemptCount:      domainNode.BootAttemptCount,
		BootFailsafeThreshold: domainNode.BootFailsafeThreshold,
		WatchdogDisarmed:      domainNode.WatchdogDisarmReason != "",
		WatchdogDisarmReason:  domainNode.WatchdogDisarmReason,
		// Master token authentication fields
		ApprovalStatus:       string(domainNode.ApprovalStatus),
		MasterTokenID:        domainNode.MasterTokenID,
		AwaitingFirstConnect: domainNode.AwaitingFirstConnect,
		// DefaultPassword is intentionally NOT included - never returned in API responses
		CreatedAt: domainNode.CreatedAt,
		UpdatedAt: domainNode.UpdatedAt,
	}

	// Include LastSeenAt if available
	if domainNode.LastSeenAt != nil {
		lastSeenAt := domainNode.LastSeenAt.Unix()
		result.LastSeenAt = &lastSeenAt
	}
	if domainNode.LastBootAt != nil {
		lastBootAt := domainNode.LastBootAt.Unix()
		result.LastBootAt = &lastBootAt
	}

	result.BootloaderVersion = domainNode.BootloaderVersion
	if domainNode.BootloaderTimestamp != nil {
		ts := domainNode.BootloaderTimestamp.Unix()
		result.BootloaderTimestamp = &ts
	}
	if domainNode.BootloaderFlashedAt != nil {
		ts := domainNode.BootloaderFlashedAt.Unix()
		result.BootloaderFlashedAt = &ts
	}
	result.BootloaderRebootPending = domainNode.BootloaderRebootPending()
	if domainNode.BootFailsafeAt != nil {
		ts := domainNode.BootFailsafeAt.Unix()
		result.BootFailsafeAt = &ts
	}

	// Include storage devices if present
	if len(domainNode.StorageDevices) > 0 {
		result.StorageDevices = domainNode.StorageDevices
	}

	// Include system info if present
	if domainNode.SystemInfo != nil {
		result.SystemInfo = domainNode.SystemInfo
	}

	// Boot order is already stored as array
	result.BootOrder = domainNode.BootOrder
	if result.BootOrder == nil {
		result.BootOrder = []string{}
	}

	return result
}

// NodeListResult is a list of nodes in API responses.
//
// It carries a COUNT and no limit/offset. Those two were here, set to
// (len(items), 0) at the only call site — a client reading them as a page size
// and an offset would page forever over the same whole list, believing it was
// paginating. An unpaginated list saying so is the honest shape; when this list
// does gain paging it takes the `page`/`per_page`/`total` envelope the audit,
// execution and secret lists already use, not a second convention.
type NodeListResult struct {
	Nodes []NodeResult `json:"nodes"`
	Count int          `json:"count"`
}

// NewNodeListResult builds the list envelope.
func NewNodeListResult(nodes []NodeResult) *NodeListResult {
	return &NodeListResult{
		Nodes: nodes,
		Count: len(nodes),
	}
}

// NodePageResult is the paginated node list envelope — the page/per_page/total
// convention the audit, execution and secret lists already use (the shape
// TestUnpaginatedListsPublishNoPagingFields demands when a list gains paging).
// Served only when the caller asks for a page; a bare GET /nodes keeps the
// legacy NodeListResult, so pre-pagination consumers never see this shape.
type NodePageResult struct {
	Nodes   []NodeResult `json:"nodes"`
	Total   int          `json:"total"`
	Page    int          `json:"page"`
	PerPage int          `json:"per_page"`
}

// NewNodePageResult builds one page of the node list. Total counts the rows
// the caller may SEE (post-authz, post-filter), never the account's row count.
func NewNodePageResult(nodes []NodeResult, total, page, perPage int) *NodePageResult {
	return &NodePageResult{
		Nodes:   nodes,
		Total:   total,
		Page:    page,
		PerPage: perPage,
	}
}
