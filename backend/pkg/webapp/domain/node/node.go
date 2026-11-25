package node

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
)

// HardwareFingerprint represents the hardware identification of a node
type HardwareFingerprint struct {
	MacAddress     string `json:"mac_address"`
	HardwareSerial string `json:"hardware_serial"` // Hardware serial number for additional entropy
}

// StoragePartition represents a partition on a storage device
type StoragePartition struct {
	PartitionID    string `json:"partition_id"`    // Partition identifier (e.g., "/dev/sda1", "nvme0n1p1")
	MountPoint     string `json:"mount_point"`     // Mount point (if mounted), empty if not mounted
	Filesystem     string `json:"filesystem"`      // Filesystem type (e.g., "ext4", "xfs", "ntfs", "")
	UsedSpace      uint64 `json:"used_space"`      // Used space in bytes
	AvailableSpace uint64 `json:"available_space"` // Available space in bytes
}

// StorageDevice represents storage device metadata
type StorageDevice struct {
	Path          string             `json:"path"`           // Device path (e.g., "/dev/sda")
	Label         string             `json:"label"`          // Human-readable label/name
	Model         string             `json:"model"`          // Device model name
	Vendor        string             `json:"vendor"`         // Device vendor/manufacturer
	Serial        string             `json:"serial"`         // Serial number
	DeviceType    string             `json:"device_type"`    // Device type: "ssd", "hdd", "nvme", "usb", etc.
	TotalCapacity uint64             `json:"total_capacity"` // Total capacity in bytes
	IsRemovable   bool               `json:"is_removable"`   // Whether the device is removable
	IsReadOnly    bool               `json:"is_read_only"`   // Whether the device is read-only
	Partitions    []StoragePartition `json:"partitions"`     // Array of partitions on this device
}

// NetworkInfo represents network information
type NetworkInfo struct {
	IP       string `json:"ip"`       // Primary IP address
	Hostname string `json:"hostname"` // System hostname
}

// BootInfo represents boot information
type BootInfo struct {
	CurrentBootDevice string   `json:"current_boot_device,omitempty"` // Current boot device: "sd", "usb", "nvme", "http", or empty if unknown
	BootDevicesOrder  []string `json:"boot_devices_order,omitempty"`  // Boot order array: ["nvme", "sd", "usb", "restart"]
	BootedAt          *int64   `json:"booted_at,omitempty"`           // Unix timestamp (seconds since epoch) when the system booted
	UptimeSeconds     *int64   `json:"uptime_seconds,omitempty"`      // Seconds since boot (clock-independent; reliable on RTC-less/netbooted devices)
	BootHTTPHost      string   `json:"boot_http_host,omitempty"`      // EEPROM-baked HTTP_HOST (the netboot server this device fetches from)
	BootHTTPPath      string   `json:"boot_http_path,omitempty"`      // EEPROM-baked HTTP_PATH read back from the running EEPROM; what phase-2 httpboot enforcement confirms a flash against
	// BootloaderVersion/BootloaderTimestamp are the RUNNING EEPROM firmware as
	// reported by the agent (works on NAT'd/steady-state nodes that never
	// HTTP-boot, unlike the httpboot handler's UA-captured value).
	BootloaderVersion   string `json:"bootloader_version,omitempty"`
	BootloaderTimestamp int64  `json:"bootloader_timestamp,omitempty"`
	// RecoveryBootstrap reports that the node is running the RAM-backed recovery
	// image rather than an installed OS. CurrentBootDevice cannot substitute for
	// it: the same bootstrap reports "http" when netbooted and "sd" when booted
	// from a provisioning card. Agents older than 0.4.5 never set it, so the
	// boot-device test stays as the fallback.
	RecoveryBootstrap bool `json:"recovery_bootstrap,omitempty"`
	// RootDevice is the whole disk the node is running from ("/dev/nvme0n1"),
	// empty when there is none to name — the recovery bootstrap's normal case,
	// since its root is RAM — and empty on agents older than 0.5.5.
	//
	// CurrentBootDevice cannot substitute: it is a CLASS from the ROM boot-mode
	// byte and cannot separate two devices of the same kind, which is the
	// ambiguity that makes an operator wipe the wrong medium.
	RootDevice string `json:"root_device,omitempty"`
	// RootPersistence is how the root filesystem keeps writes: "persistent",
	// "overlay" (overlayroot — writes land in a tmpfs upper layer and
	// evaporate at reboot), or "" from an agent that predates the field or a
	// RAM-backed recovery root (which RecoveryBootstrap already covers).
	// Write-shaped dispatch is REFUSED on "overlay": an apply that evaporates
	// while its recorded hash suppresses the re-apply is worse than a refusal,
	// and the worst member of the family is a key revocation that reads as
	// done. "" changes nothing — an old agent keeps today's behaviour.
	RootPersistence string `json:"root_persistence,omitempty"`
}

// HardwareInfo represents hardware information
type HardwareInfo struct {
	CPU    *CPUInfo    `json:"cpu,omitempty"`    // CPU information
	RAM    *RAMInfo    `json:"ram,omitempty"`    // RAM information
	TPM    *TPMInfo    `json:"tpm,omitempty"`    // TPM (Trusted Platform Module) information
	Boot   *BootInfo   `json:"boot,omitempty"`   // Boot information (current device + order)
	EEPROM *EEPROMInfo `json:"eeprom,omitempty"` // EEPROM information (Raspberry Pi only)
	// Power is the SoC's power/thermal throttling state. Nil means the device
	// could not answer (not a Pi, no vcgencmd, or an agent older than 0.5.4) —
	// which is deliberately different from a zeroed reading, because "the supply
	// is fine" and "we have no idea" must not look the same.
	Power *PowerInfo `json:"power,omitempty"`
	// USB lists attached non-storage USB devices. Empty is normal: hardware with
	// no USB bus, and every agent older than 0.5.5. Storage is deliberately
	// absent — a USB disk is already a StorageDevice.
	USB []USBDevice `json:"usb,omitempty"`
	// HAT is the spec-compliant HAT ID EEPROM, nil when no HAT is present
	// (agents >= 0.6.0; older agents simply never send it).
	HAT *HATInfo `json:"hat,omitempty"`
}

// USBDevice is one thing plugged into a USB port: a Wi-Fi dongle, a serial or
// CAN adapter, a HAT that presents over USB. Hubs and mass storage are filtered
// out on the device — a hub is structure rather than an attachment, and a disk
// is already reported as a StorageDevice.
type USBDevice struct {
	VendorID     string `json:"vendor_id"`
	ProductID    string `json:"product_id"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	// ClassCode is the USB-IF class of the device or its first interface. Raw on
	// the wire and named here, the same division as CPUInfo.BoardRevision: the
	// table belongs to USB-IF, so a new class becomes readable with a backend
	// deploy rather than a fleet-wide agent roll.
	ClassCode uint32 `json:"class_code"`
	Speed     string `json:"speed,omitempty"` // Mbit/s as the kernel reports it
	Port      string `json:"port,omitempty"`  // bus path; what tells two identical dongles apart
}

// USBClassName renders a USB class code for an operator. Unknown codes are
// shown as hex rather than as "Unknown", because the number is something a
// support conversation can look up and a word is not.
func USBClassName(code uint32) string {
	switch code {
	case 0x01:
		return "Audio"
	case 0x02:
		return "Communications"
	case 0x03:
		return "HID"
	case 0x05:
		return "Physical"
	case 0x06:
		return "Image"
	case 0x07:
		return "Printer"
	case 0x0a:
		return "CDC data"
	case 0x0b:
		return "Smart card"
	case 0x0d:
		return "Content security"
	case 0x0e:
		return "Video"
	case 0x0f:
		return "Healthcare"
	case 0xe0:
		return "Wireless"
	case 0xef:
		return "Miscellaneous"
	case 0xfe:
		return "Application specific"
	case 0xff:
		return "Vendor specific"
	default:
		return fmt.Sprintf("Class 0x%02x", code)
	}
}

// CPUInfo represents CPU information
type CPUInfo struct {
	Model  string `json:"model"`  // CPU model name
	Vendor string `json:"vendor"` // CPU vendor (e.g., "Intel", "AMD", "ARM")
	Cores  uint32 `json:"cores"`  // Number of CPU cores
	Info   string `json:"info"`   // Additional CPU information (architecture, features, etc.)
	// BoardRevision is the raw Raspberry Pi board revision code (e.g. "c04170"),
	// empty on non-Pi hardware and on agents older than 0.4.5. Prefer it over
	// Model for anything board-specific: Model comes from the device tree, and
	// the recovery bootstrap forces one device_tree= for all BCM2712, so every
	// Pi 5 in recovery calls itself a Compute Module 5.
	BoardRevision string `json:"board_revision,omitempty"`
}

// RAMInfo represents RAM information
type RAMInfo struct {
	Size uint64 `json:"size"` // Total RAM size in bytes
}

// TPMInfo represents TPM (Trusted Platform Module) information
type TPMInfo struct {
	Present     bool   `json:"present"`     // Whether TPM is present
	Version     string `json:"version"`     // TPM version (e.g., "2.0", "1.2") if available
	Model       string `json:"model"`       // TPM module model (e.g., "SLB9670") if available
	Description string `json:"description"` // TPM description/additional info if available
}

// EEPROMInfo represents EEPROM information (Raspberry Pi only)
type EEPROMInfo struct {
	Enabled     bool   `json:"enabled"`               // Whether EEPROM updates are enabled
	Description string `json:"description,omitempty"` // Additional info (e.g., "CM4 EEPROM updates enabled")
}

// HATInfo is a spec-compliant HAT's ID EEPROM identity, as the firmware
// exposed it in the device tree. nil on HardwareInfo = no HAT present.
type HATInfo struct {
	Vendor     string `json:"vendor,omitempty"`
	Product    string `json:"product,omitempty"`
	ProductID  string `json:"product_id,omitempty"`
	ProductVer string `json:"product_ver,omitempty"`
	UUID       string `json:"uuid,omitempty"`
}

// SanitizeHATField bounds and cleans one HAT EEPROM string: any vendor
// programs that EEPROM, and the value is stored in system_info jsonb and
// rendered in the UI. Control characters are dropped, length is capped.
func SanitizeHATField(v string) string {
	const maxLen = 128
	v = strings.TrimSpace(v)
	var b strings.Builder
	for _, r := range v {
		if r >= 32 && r != 127 {
			b.WriteRune(r)
		}
		if b.Len() >= maxLen {
			break
		}
	}
	return b.String()
}

// OSInfo represents operating system information
type OSInfo struct {
	Version string `json:"version"` // OS version
	Details string `json:"details"` // OS distribution details (e.g., "Ubuntu 22.04 LTS")
}

// SystemInfo represents comprehensive system information
type SystemInfo struct {
	Network        *NetworkInfo              `json:"network,omitempty"`         // Network information
	Hardware       *HardwareInfo             `json:"hardware,omitempty"`        // Hardware information
	OS             *OSInfo                   `json:"os,omitempty"`              // OS information
	StorageDevices map[string]*StorageDevice `json:"storage_devices,omitempty"` // Storage devices map
}

// IdentifyState is one node's transient identify/button state (the
// platform's in-memory registry; see services/node/identify_state.go).
type IdentifyState struct {
	NodeID     uuid.UUID
	BlinkUntil *time.Time
	PressedAt  *time.Time
}

// NodeStatus represents the status of a node
type NodeStatus string

// Status constants for node status
const (
	StatusReadyForAdoption NodeStatus = "ready_for_adoption"
	// StatusFlashing marks a WebUSB-provisioned node whose EEPROM write is still
	// in progress: the node record must exist before the flash (its adoption
	// token is baked into the EEPROM), but it is only revealed as
	// ready_for_adoption once the browser confirms the flash completed. A node
	// left in this state signals an interrupted flash that should be retried.
	StatusFlashing    NodeStatus = "flashing"
	StatusMaintenance NodeStatus = "maintenance"
	StatusOnline      NodeStatus = "online"
	StatusOffline     NodeStatus = "offline"
	StatusRestarting  NodeStatus = "restarting"
	StatusShutdown    NodeStatus = "shutdown"
)

// ApprovalStatus represents the approval state of a node (for master token auth)
type ApprovalStatus string

// Approval status constants
const (
	ApprovalStatusApproved        ApprovalStatus = "approved"
	ApprovalStatusPendingApproval ApprovalStatus = "pending_approval"
	ApprovalStatusRejected        ApprovalStatus = "rejected"
)

// Node represents a node in the system
type Node struct {
	ID              uuid.UUID
	AccountID       uuid.UUID
	Name            string
	Hostname        string
	IPv4            string
	MacAddress      string
	Fingerprint     *HardwareFingerprint // Nullable fingerprint
	UniqueToken     string               // Unique token for node registration
	Status          NodeStatus
	LastHeartbeat   *time.Time // Timestamp of last heartbeat
	AgentVersion    string     // Version of the agent software
	AgentKind       string     // ""/"full" = managing agent; "watcher" = telemetry-only, never dispatched commands
	AgentInstanceID string     // instance id of the agent PROCESS last seen connecting; "" until a kind-reporting agent connects
	// LEDSources is what the node's agent advertised it can render as ambient
	// LED indication (ConnectionInit led_sources). Empty = no advertisement —
	// an agent from before LED v2 — which SupportedLEDSources treats as the
	// v1 pair rather than "nothing", or every existing config would refuse.
	LEDSources []string
	// PinnedBySlug names the command that put the node into its current
	// transient status, or "" when nothing did.
	//
	// It exists because "restarting" answers two different questions with one
	// word. The pin is set at DISPATCH for every slug that MIGHT take the device
	// down, deliberately, because the agent cannot be trusted about a reboot it
	// has not performed yet (node_command.CausesReboot). For a bootloader flash
	// that claim is usually false: the device stays up, and an operator reading
	// "restarting" is told the machine is going down when it is not. Recording
	// the cause lets a reader say "working" for a pin no reboot was asked for,
	// and keep the alarming word for an actual reboot.
	//
	// A COLUMN rather than a join against the command log because
	// results.NewNodeResult is called from sixteen places, including every
	// WebSocket push, and a field only some of them could fill would blink out
	// of the UI on the next frame.
	//
	// Meaningful only while the status is one a command imposed. Every other
	// status write clears it, at the repository's single choke point.
	PinnedBySlug    string
	LastSeenAt      *time.Time                // Last time node was seen (any activity)
	StorageDevices  map[string]*StorageDevice // Map of device paths to storage device metadata (kept for backward compatibility)
	SystemInfo      *SystemInfo               // Comprehensive system information (network, hardware, OS, storage)
	BootOrder       []string                  // Boot order as array of device enums (e.g., ["nvme", "sd", "usb", "restart"])
	SSHKeyID        *uuid.UUID                // SSH key ID for provisioning (nullable)
	DefaultUsername string                    // Default username for provisioning
	DefaultPassword string                    // Default password for provisioning: ENCRYPTED at rest, never returned in API responses
	// DefaultPasswordUnreadable is set by the repository when a stored password
	// exists but could not be decrypted with any configured key. DefaultPassword
	// is then empty, and the two must be read together: an empty password means
	// "none set", an empty password with this flag means "set but unrecoverable".
	// Resolution must stop at such a level rather than fall through to the
	// parent's password, and a write must leave the column alone rather than
	// overwrite a ciphertext a rotation could still recover. Never persisted.
	DefaultPasswordUnreadable bool

	// UniqueTokenUnreadable is the token's twin of DefaultPasswordUnreadable:
	// a stored ciphertext no configured key can read. UniqueToken is then
	// empty, and the connect path refuses an unreadable or empty token before
	// comparing — the honest outcome of a lost key — and a write must leave the column
	// alone rather than replace a ciphertext a rotation could still recover.
	// Never persisted.
	UniqueTokenUnreadable bool

	// Notes is free-form operator text about this node (rack position, owner,
	// what was swapped and when). Purely descriptive: nothing on the device or
	// in the platform reads it, and it is the one node field the fleet-list
	// search box matches that the device itself never reports.
	Notes string

	// Organizational unit membership and per-node policy overrides.
	// A nil policy pointer means "inherit" (from the OU, then root OU, then default).
	OUID            *uuid.UUID // Organizational unit the node belongs to (nil = unassigned)
	NetworkFirst    *bool      // Override: boot the network bootstrap first
	BootToOS        *bool      // Override: hand off to the installed OS on NVMe
	SSHPasswordAuth *bool      // Override: accept sshd password logins (nil = inherit)
	OSImageID       *uuid.UUID // Override: OS image for provisioning (nil = inherit)

	// One-shot netboot (reboot-to-recovery): while pending, BootToOS has been
	// forced to netboot for a single reboot and RecoveryPrevBootToOS holds the
	// value to restore ("inherit", "on" or "off") on the next reconnect.
	RecoveryRevertPending bool

	// AwaitingReturnAfter / AwaitingReturnSince mark a node the platform
	// expects back: a successful install_os settles into a self-reboot, and
	// the unreachable sweeper records what the node was doing when it stopped
	// answering. Cleared on reconnect. While the node is OFFLINE with the
	// stamp set, "offline" is the wrong word for what happened — the honest
	// one is "never came back after <slug>", and the UI says so.
	AwaitingReturnAfter  string
	AwaitingReturnSince  *time.Time
	RecoveryPrevBootToOS string

	// Bootloader (EEPROM firmware) identity as reported by the ROM's HTTP-boot
	// request User-Agent on the device's last boot attempt. Network-first
	// devices hit /http-boot on every boot, so this tracks the currently
	// running bootloader even when the device boots its local OS.
	BootloaderVersion   string     // short firmware build hash, e.g. "f68405bc"
	BootloaderTimestamp *time.Time // firmware build timestamp (nil if unreported)

	// BootloaderFlashedAt is when the node's most recent successful EEPROM
	// flash (update_boot_order) settled, on the SERVER's clock — stamped by
	// the gRPC settle hook, the device-profile pattern. The EEPROM is written
	// at command time and read at boot time, and that gap is the "flashed,
	// reboot still owed" state the UI must keep showing after the panel
	// closes. Never cleared: the comparison against LastBootAt
	// is what retires it.
	BootloaderFlashedAt *time.Time

	// Boot-loop failsafe: a network-first node that keeps re-netbooting without a
	// successful OS boot is crash-looping (broken OS/agent + a watchdog reboot).
	// The httpboot handler counts consecutive WITHHELD boot.sig requests (the only
	// file the ROM fetches when withheld); after the effective threshold it SERVES
	// recovery so the node becomes inspectable, sets BootFailsafeAt, and raises an
	// event. The counter resets on a confirmed OS boot (current_boot_device=nvme),
	// operator acknowledge, or operator boot-intent (boot_to_os change / install_os
	// / reboot-to-recovery).
	BootAttemptCount  int        // consecutive withheld netboot attempts since the last confirmed OS boot
	LastBootAttemptAt *time.Time // when the last withheld netboot attempt was counted (window/debounce anchor)
	BootFailsafeAt    *time.Time // set when the failsafe last served recovery; drives the UI badge until cleared
	// WatchdogConfig is the node's desired watchdog supervision (nil = never
	// configured). WatchdogAppliedHash is what the device last confirmed, so a
	// desired/applied mismatch is visible; WatchdogDisarmReason is set when the
	// agent's local breaker took the watchdog down on its own.
	// WatchdogLastRefusal records the agent's pre-arm verification declining a
	// config ("<code>: <details>") — its own fact, distinct from a disarm: a
	// refusal means the device never armed, a disarm means it un-armed itself.
	WatchdogConfig *WatchdogConfig
	// LEDConfig is the node's desired LED behaviour (nil = never configured);
	// LEDAppliedHash is what the device last confirmed (see led.go).
	LEDConfig            *LEDConfig
	LEDAppliedHash       string
	WatchdogAppliedHash  string
	WatchdogDisarmReason string
	WatchdogLastRefusal  string

	// DeviceProfile is this node's own declared hardware equipage (nil =
	// unassigned; the OU/account chain may still provide one — resolution is
	// the OU service's ResolveEffectiveDeviceProfile). Three separate columns
	// with one writer each, on the watchdog model: the equipage is written by
	// the assignment endpoint, the applied hash only by the apply settle hook
	// (recording the hash the dispatch PAYLOAD carried, never one recomputed
	// later), and the scan result only by the scan poll.
	DeviceProfile            *device_profile.Equipage
	DeviceProfileAppliedHash string
	DeviceProfileScan        *device_profile.ScanResult
	// DeviceProfileAppliedAt is when that hash was recorded, on the SERVER's
	// clock. It exists to answer "has the node booted since the apply?", which
	// is what decides whether a reboot is still owed — an overlay only takes
	// effect on the next boot, and without this the UI offered Reboot forever
	// after any apply.
	DeviceProfileAppliedAt *time.Time

	// LastBootAt is when this node last booted, on the SERVER's clock,
	// computed from the agent's uptime rather than read from its
	// BootInfo.BootedAt. That is not a detail: the device's wall clock is
	// exactly what a missing or blank hardware RTC gets wrong — such a node
	// boots into the year 2000 — and a boot moment taken from it
	// would sit permanently before every apply, which is the "reboot still
	// owed" answer inverted. Uptime is clock-independent.
	// Nil = never reported (older agent) = unknown, never "has not booted".
	LastBootAt *time.Time

	// AccessAppliedGeneration is the access-config hash the DEVICE reported
	// having fully applied, and AccessAppliedAt is when. The desired hash is
	// computed fresh at dispatch time and stored nowhere, so this is the only
	// half of the comparison that can be persisted — and without it the hash the
	// agent has been echoing back since the feature shipped had no reader, which
	// is why the self-heal it was built for never happened.
	//
	// Empty means access management has never fully converged on this node:
	// either it was never applied, or the last apply was partial. Both must read
	// as "do not compare", never as "out of date" — a node access never touched
	// must not be pushed a configuration by a reconnect.
	AccessAppliedGeneration string
	AccessAppliedAt         *time.Time
	BootFailsafeThreshold   *int // per-node override for the failsafe threshold (nil = use the global default; 0 = disabled)

	// Master token authentication fields
	ApprovalStatus ApprovalStatus // Approval status for master token nodes (approved, pending_approval, rejected)
	MasterTokenID  *uuid.UUID     // Master token used for registration (nullable) - if present, node uses master token auth

	// AwaitingFirstConnect marks a node that was provisioned by a browser WebUSB
	// EEPROM flash: its hardware is known and it is already approved, it is just
	// waiting to reboot, netboot and connect for the first time. Cleared on the
	// first agent connect. Drives a distinct "flashed, awaiting first boot" UI
	// instead of the SD-centric ready-for-adoption card.
	AwaitingFirstConnect bool

	// AutoProvisionEEPROMPending is a one-shot flag: when set, the platform flashes
	// a network-first EEPROM the first time the agent reports full system info (SoC
	// and current boot device become known only then). It is cleared right after the
	// flash is dispatched (or after the guards decide to skip) so it never retriggers
	// on subsequent reconnects, which would cause a reflash-reboot loop. Set at node
	// creation when the "single node" flow requests network-first provisioning.
	AutoProvisionEEPROMPending bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectiveBootFailsafeThreshold resolves the boot-loop-failsafe threshold for
// this node: the per-node override when set (0 = disabled), else the install's
// global default. Shared by the netboot withhold path and the watchdog
// recovery_limited flag so the two can never disagree about whether the
// failsafe is armed.
func (n *Node) EffectiveBootFailsafeThreshold(global int) int {
	if n.BootFailsafeThreshold != nil {
		return *n.BootFailsafeThreshold
	}
	return global
}

// NewNode creates a new Node instance with default values
func NewNode(accountID uuid.UUID, name, hostname, ipv4, macAddress string) *Node {
	now := time.Now()
	return &Node{
		ID:              uuid.New(),
		AccountID:       accountID,
		Name:            name,
		Hostname:        hostname,
		IPv4:            ipv4,
		MacAddress:      macAddress,
		Fingerprint:     nil, // Will be set during registration
		UniqueToken:     uuid.New().String(),
		Status:          StatusOffline,
		StorageDevices:  make(map[string]*StorageDevice),
		SystemInfo:      nil, // Will be set during system info updates
		SSHKeyID:        nil,
		DefaultUsername: "",
		DefaultPassword: "",
		ApprovalStatus:  ApprovalStatusApproved, // Default to approved for node_token auth
		MasterTokenID:   nil,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// NewNodeWithID creates a new Node instance with a specific ID
func NewNodeWithID(id, accountID uuid.UUID, name, hostname, ipv4, macAddress string) *Node {
	now := time.Now()
	return &Node{
		ID:              id,
		AccountID:       accountID,
		Name:            name,
		Hostname:        hostname,
		IPv4:            ipv4,
		MacAddress:      macAddress,
		Fingerprint:     nil, // Will be set during registration
		UniqueToken:     uuid.New().String(),
		Status:          StatusOffline,
		StorageDevices:  make(map[string]*StorageDevice),
		SystemInfo:      nil, // Will be set during system info updates
		SSHKeyID:        nil,
		DefaultUsername: "",
		DefaultPassword: "",
		ApprovalStatus:  ApprovalStatusApproved, // Default to approved for node_token auth
		MasterTokenID:   nil,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// NewNodeFromMasterToken creates a new Node instance for master token authentication
// The hardware serial from fingerprint is used as the unique identifier for the node
func NewNodeFromMasterToken(accountID, masterTokenID uuid.UUID, fingerprint HardwareFingerprint) *Node {
	now := time.Now()
	return &Node{
		ID:          uuid.New(),
		AccountID:   accountID,
		Name:        "", // Will be set by caller
		Hostname:    "",
		IPv4:        "",
		MacAddress:  fingerprint.MacAddress,
		Fingerprint: &fingerprint,
		// Master-token nodes don't authenticate via unique_token, but the DB
		// column has NOT NULL + a unique index. An empty string collides as soon
		// as a second master-token node is created, so give each a unique value.
		UniqueToken:     uuid.NewString(),
		Status:          StatusOffline,
		StorageDevices:  make(map[string]*StorageDevice),
		SystemInfo:      nil,
		SSHKeyID:        nil,
		DefaultUsername: "",
		DefaultPassword: "",
		ApprovalStatus:  ApprovalStatusPendingApproval, // Pending approval for master token
		MasterTokenID:   &masterTokenID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// CanRegister checks if the node is in a state that allows registration
func (n *Node) CanRegister() bool {
	return n.Status == StatusReadyForAdoption
}

// IsAdopted checks if the node has completed the adoption process
// IsInConnectedState reports whether the platform can still reach the device —
// which is what decides whether deleting it will strand it. It covers online AND
// the two connected-but-busy states: a node wedged mid-install sits in
// maintenance or restarting while its stream is very much alive, and keying only
// on online (as the former IsAdopted did) let the most common delete — of a node
// stuck part-way through an install — skip both the force gate and the
// release_identity dispatch, stranding the device.
func (n *Node) IsInConnectedState() bool {
	return n.Status == StatusOnline ||
		n.Status == StatusMaintenance ||
		n.Status == StatusRestarting
}

// IsPendingApproval checks if the node is waiting for approval
func (n *Node) IsPendingApproval() bool {
	return n.ApprovalStatus == ApprovalStatusPendingApproval
}

// IsApproved checks if the node has been approved
func (n *Node) IsApproved() bool {
	return n.ApprovalStatus == ApprovalStatusApproved
}

// IsRejected checks if the node has been rejected
func (n *Node) IsRejected() bool {
	return n.ApprovalStatus == ApprovalStatusRejected
}

// NotAdmittedStatuses are the statuses of a row whose device has never
// reported in: an operator-created node waiting for its agent, and a
// browser-flashed or SSH-adopted placeholder waiting for its first boot. The
// device entitlement's count excludes them; the SQL count and IsAdmitted both
// read this list so they cannot disagree.
var NotAdmittedStatuses = []NodeStatus{StatusReadyForAdoption, StatusFlashing}

// IsPreConnectStatus reports whether s is one of NotAdmittedStatuses. Those
// statuses are PLATFORM-set — a node created for an agent that has not
// called in, a browser flash in progress — and never reported by a device.
func IsPreConnectStatus(s NodeStatus) bool {
	for _, p := range NotAdmittedStatuses {
		if s == p {
			return true
		}
	}
	return false
}

// AdmissionRequiredError is a plain write's refusal to carry an approved row
// out of a pre-connect status into an admitted one. That transition is the
// ADMISSION the device cap bounds, so it is only ever made by the locked
// admission (count and write in one transaction); a write that is not that
// one returns this instead, naming the row so the caller can take the lock.
type AdmissionRequiredError struct {
	NodeID    uuid.UUID
	AccountID uuid.UUID
	Stored    NodeStatus
	Requested NodeStatus
}

func (e *AdmissionRequiredError) Error() string {
	return fmt.Sprintf("node %s: moving it from %q to %q admits it, which only the device-cap admission may do",
		e.NodeID, e.Stored, e.Requested)
}

// IsAdmitted reports whether this row is a device under management for the
// purpose of the device entitlement: approved and not sitting in a
// pre-connect status. A pending device, a rejected one or a placeholder is
// not a device the install manages, and a leaked master token must not be
// able to fill a paid cap with rows that never connected.
//
// ADMISSION is the transition of this predicate from false to true, and the
// cap is enforced there: a row leaves a pre-connect status only on its
// device's first connect (Connect, the master-token placeholder reconcile),
// and a pending row becomes approved only through ApproveNode — each under
// the cap. Any other write that would make the same move — a status write,
// a full-row save from a stale read — is refused by the repository with
// AdmissionRequiredError and, for a status write, retried under the cap.
// AwaitingFirstConnect is deliberately not part of the predicate: a node
// restored ADMITTED (it was admitted when deleted; status offline, awaiting
// its device's rebind) was admitted by the restore itself, under the cap,
// and is counted from then, so its rebind is not a second admission. A node that was NOT admitted when deleted is restored in its
// original non-admitted state (node_tombstone.Tombstone.RestoreState), so
// its rebind is.
func (n *Node) IsAdmitted() bool {
	if !n.IsApproved() {
		return false
	}
	for _, s := range NotAdmittedStatuses {
		if n.Status == s {
			return false
		}
	}
	return true
}

// Approve approves a pending node
func (n *Node) Approve() {
	n.ApprovalStatus = ApprovalStatusApproved
	n.UpdatedAt = time.Now()
}

// Reject rejects a pending node
func (n *Node) Reject() {
	n.ApprovalStatus = ApprovalStatusRejected
	n.UpdatedAt = time.Now()
}

// UsesMasterToken checks if the node uses master token authentication
func (n *Node) UsesMasterToken() bool {
	return n.MasterTokenID != nil
}

// GetHardwareSerial returns the hardware serial from fingerprint (used as unique identifier)
func (n *Node) GetHardwareSerial() string {
	if n.Fingerprint != nil {
		return n.Fingerprint.HardwareSerial
	}
	return ""
}

// String returns the string representation of the status
func (s NodeStatus) String() string {
	return string(s)
}

// MaxNotesLength bounds the free-form operator notes. The column is unbounded
// text; the cap is about the read path, not storage — notes ship inside every
// NodeResult, and the fleet list returns one of those per node, so a pasted log
// file would be paid for on every list request by every user of the account.
const MaxNotesLength = 4096

// ErrNotesTooLong is returned when node notes exceed MaxNotesLength.
var ErrNotesTooLong = fmt.Errorf("notes must be at most %d characters", MaxNotesLength)

// ValidateNotes checks the free-form operator notes. Counting runes rather than
// bytes keeps the limit meaning the same for a Cyrillic note as for an ASCII one.
func ValidateNotes(notes string) error {
	if utf8.RuneCountInString(notes) > MaxNotesLength {
		return ErrNotesTooLong
	}
	return nil
}

// ValidateBootOrderArray validates that all devices in the array are valid boot device enums
func ValidateBootOrderArray(order []string) error {
	validDevices := GetValidBootOptionValues()

	for _, device := range order {
		device = strings.ToLower(strings.TrimSpace(device))
		if !validDevices[device] {
			// Get all valid values for error message
			allOptions := GetAllBootOptions()
			validValues := make([]string, 0, len(allOptions))
			for _, opt := range allOptions {
				validValues = append(validValues, opt.Value)
			}
			return fmt.Errorf("invalid boot device: %s (must be one of: %s)", device, strings.Join(validValues, ", "))
		}
	}

	return nil
}

// NetworkFirstBootOrder is http -> nvme -> usb -> sd -> restart (0xf1467): the
// device netboots first, falls back to local storage, and the platform controls
// serve/withhold.
//
// USB boot is for EVERY board, not a Pi 4 B workaround: CM4, CM4S, CM5, Pi 4 B
// and Pi 5 all boot a USB disk, and installing to an external SSD is a normal
// choice on any of them. It sits between NVMe and SD deliberately — a board
// booting from NVMe never reaches the digit, so nothing changes there, while
// everything else reaches its disk instead of falling through to SD, which is
// the provisioning card the operator is told to remove after onboarding.
// Without this digit "install to an SSD" produces a disk the device can write
// and never boot.
//
// There is no SATA digit in the Raspberry Pi BOOT_ORDER: a SATA SSD boots
// through a USB-SATA bridge and is therefore this same digit, while a PCIe SATA
// HBA is not bootable by the ROM at all (NVMe is the only PCIe boot path).
// bcm_usb_msd (5) is deliberately NOT here — it is the Type-C/CM4IO-specific
// path, unavailable on a Pi 5, and every extra digit costs a timeout on a board
// that does not have that device.
//
// Changing this only affects EEPROMs baked AFTER the change; already-provisioned
// nodes keep their current order until someone re-runs provision-eeprom.
var NetworkFirstBootOrder = []string{"http", "nvme", "usb_msd", "sd", "restart"}

// Agent kinds, declared by the agent itself in ConnectionInit.
// An empty value means "full" — every agent that predates the field is the
// managing one.
const (
	AgentKindFull    = "full"
	AgentKindWatcher = "watcher"
)

// Root-persistence facts as reported in BootInfo.RootPersistence.
const (
	RootPersistencePersistent = "persistent"
	RootPersistenceOverlay    = "overlay"
)

// IsWatcher reports whether this node runs the telemetry-only agent: it
// carries no command-execution code, so the platform must never dispatch a
// command, open a terminal, or queue anything for it. Management happens by
// netbooting into recovery (where the FULL recovery agent runs) — see the
// apply_access_config_offline path.
func (n *Node) IsWatcher() bool {
	return n.AgentKind == AgentKindWatcher
}

// CurrentBootDevice returns the device the node last booted from
// ("sd"/"nvme"/"http"/...), or "" if unknown. Populated from SystemInfo, which
// the agent reports only after the initial connection handshake.
func (n *Node) CurrentBootDevice() string {
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.Boot != nil {
		return n.SystemInfo.Hardware.Boot.CurrentBootDevice
	}
	return ""
}

// RootPersistenceFact reports how the node's root filesystem keeps writes:
// "persistent", "overlay", or "" (agent predates the field, or a RAM-backed
// recovery root, which IsRecoveryBootstrap covers separately).
func (n *Node) RootPersistenceFact() string {
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.Boot != nil {
		return n.SystemInfo.Hardware.Boot.RootPersistence
	}
	return ""
}

// BootMomentFrom converts a report's uptime into a boot moment on the
// OBSERVER's clock. observedAt must be when the report arrived, not "now" at
// some later read: uptime is a snapshot, so pairing a stale uptime with a fresh
// clock walks the computed boot moment forward and would eventually claim any
// node had just rebooted.
//
// Returns false when the agent reported no usable uptime (older agents, and the
// zero value on a report that omits it), because "unknown" and "has not
// rebooted" must not collapse into the same answer.
func BootMomentFrom(uptimeSeconds *int64, observedAt time.Time) (time.Time, bool) {
	if uptimeSeconds == nil || *uptimeSeconds <= 0 {
		return time.Time{}, false
	}
	return observedAt.Add(-time.Duration(*uptimeSeconds) * time.Second), true
}

// ReportedUptimeSeconds is the agent's clock-independent seconds-since-boot,
// or nil when it reported none.
func (n *Node) ReportedUptimeSeconds() *int64 {
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.Boot != nil {
		return n.SystemInfo.Hardware.Boot.UptimeSeconds
	}
	return nil
}

// DeviceProfileRebootPending answers whether the applied boot config is still
// waiting for the reboot that activates it: overlays are read at boot and the
// platform never reboots by itself.
//
// Tri-state on purpose. Nil means the platform cannot tell — nothing applied
// yet, or a node that never reported an uptime — and every caller must then
// FAIL OPEN and keep offering the reboot. Withholding an action the operator
// needs is worse than offering one they do not, and the whole point of this
// function is that the old answer was a permanent, unconditional "yes".
func (n *Node) DeviceProfileRebootPending() *bool {
	if n.DeviceProfileAppliedAt == nil || n.LastBootAt == nil {
		return nil
	}
	pending := n.LastBootAt.Before(*n.DeviceProfileAppliedAt)
	return &pending
}

// BootloaderRebootPending answers whether a flashed EEPROM image is still
// waiting for the reboot that activates it. Same tri-state contract as
// DeviceProfileRebootPending: nil means the platform cannot tell — no flash
// recorded, or a node that never reported an uptime — and callers fail open.
func (n *Node) BootloaderRebootPending() *bool {
	if n.BootloaderFlashedAt == nil || n.LastBootAt == nil {
		return nil
	}
	pending := n.LastBootAt.Before(*n.BootloaderFlashedAt)
	return &pending
}

// IsRecoveryBootstrap reports whether the node is running the RAM-backed
// recovery image rather than an installed OS.
//
// This is the question every "hold it until the node is on a managed OS" guard
// actually wants, and `CurrentBootDevice() == "http"` is a poor proxy for it:
// the SAME bootstrap reports "http" when netbooted and "sd" when booted from a
// provisioning card, while having no groupadd, no usermod, no systemd units and
// no persistent root in either case. Gating on the boot medium therefore let
// the platform push apply_access_config at an initramfs on every freshly
// SD-provisioned node, which can only ever fail.
//
// Agents from 0.4.5 report it directly. Older ones do not, so the boot-device
// test remains as the fallback — it is right for the netboot path, which is the
// only one those agents were ever provisioned through.
func (n *Node) IsRecoveryBootstrap() bool {
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.Boot != nil {
		if n.SystemInfo.Hardware.Boot.RecoveryBootstrap {
			return true
		}
	}
	return n.CurrentBootDevice() == "http"
}

// BootInfoIsRecoveryBootstrap is IsRecoveryBootstrap for a BootInfo that has
// not been stored on a Node yet — the freshly arrived system-info report, which
// is the only moment the boot state is current.
func BootInfoIsRecoveryBootstrap(b *BootInfo) bool {
	if b == nil {
		return false
	}
	return b.RecoveryBootstrap || b.CurrentBootDevice == "http"
}

// NormalizeBootloaderVersion reduces a raw `vcgencmd bootloader_version`
// "version" line to just the firmware hash token. vcgencmd appends a channel
// suffix (e.g. "f68405bc... (release)"); the freshness badge compares against
// the signer's short hash, so the suffix and any trailing words must be
// dropped or the comparison never matches (a current node reads as outdated).
// Returns the input unchanged if it is already a bare token.
func NormalizeBootloaderVersion(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// BootHTTPHost returns the netboot server baked into the node's EEPROM (the
// HTTP_HOST it fetches boot files from), or "" if unknown / not reported.
func (n *Node) BootHTTPHost() string {
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.Boot != nil {
		return n.SystemInfo.Hardware.Boot.BootHTTPHost
	}
	return ""
}

// SupportedLEDSources answers which ambient sources this node's agent can
// render: its advertisement, or the v1 pair for agents that predate LED v2.
func (n *Node) SupportedLEDSources() []string {
	if len(n.LEDSources) > 0 {
		return n.LEDSources
	}
	return LEDV1Sources
}

// BootHTTPPath returns the per-serial boot path read back from the running
// EEPROM, or "" if unknown / not reported. The phase-2 httpboot design
// enforces a per-device path secret only after this readback confirms the
// flash carried it.
func (n *Node) BootHTTPPath() string {
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.Boot != nil {
		return n.SystemInfo.Hardware.Boot.BootHTTPPath
	}
	return ""
}

// BootHostMismatch reports whether the node's EEPROM points at a boot server
// other than this install's (expectedBootHost). It is a mismatch only when both
// are known and differ — an unreported EEPROM host (older agents / non-Pi) or an
// unconfigured platform boot host is NOT a mismatch (no false positives).
func (n *Node) BootHostMismatch(expectedBootHost string) bool {
	got := n.BootHTTPHost()
	return expectedBootHost != "" && got != "" && !strings.EqualFold(got, expectedBootHost)
}

// SoC maps the reported CPU model to the SoC family the EEPROM tooling uses
// ("cm4"/"cm5"). Returns "" if it can't be determined (e.g. SystemInfo not yet
// reported).
func (n *Node) SoC() string {
	model := ""
	if n.SystemInfo != nil && n.SystemInfo.Hardware != nil && n.SystemInfo.Hardware.CPU != nil {
		model = n.SystemInfo.Hardware.CPU.Model
	}
	switch {
	case strings.Contains(model, "Compute Module 5"), strings.Contains(model, "Pi 5"):
		return "cm5"
	case strings.Contains(model, "Compute Module 4"), strings.Contains(model, "Pi 4"):
		return "cm4"
	default:
		return ""
	}
}

// BoolPolicyString renders a tri-state policy pointer as "inherit", "on" or
// "off" (the representation RecoveryPrevBootToOS stores).
func BoolPolicyString(v *bool) string {
	if v == nil {
		return "inherit"
	}
	if *v {
		return "on"
	}
	return "off"
}
