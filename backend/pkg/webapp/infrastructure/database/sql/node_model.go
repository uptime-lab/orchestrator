package sql

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// NodeModel is the SQL representation of a node
type NodeModel struct {
	ID                       uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;not null"`
	AccountID                uuid.UUID  `gorm:"column:account_id;type:uuid;index;not null"`
	Name                     string     `gorm:"column:name;size:255;not null"`
	Hostname                 *string    `gorm:"column:hostname;size:255"`                          // Nullable - set during registration
	IPv4                     *string    `gorm:"column:ipv4;size:45"`                               // Nullable - set during registration
	MacAddress               *string    `gorm:"column:mac_address;size:17"`                        // Nullable - set during registration
	Fingerprint              *string    `gorm:"column:fingerprint;type:jsonb"`                     // Nullable JSON storage for fingerprint
	UniqueToken              string     `gorm:"column:unique_token;size:255;uniqueIndex;not null"` // Device credential: ENCRYPTED at rest (see UniqueTokenEnc)
	// UniqueTokenEnc marks how UniqueToken is stored: 0 = legacy plaintext,
	// 1 = AES-GCM. A column, not a value prefix — the DefaultPasswordEnc rule.
	UniqueTokenEnc int `gorm:"column:unique_token_enc;not null;default:0"`
	Status                   string     `gorm:"column:status;size:50;not null;default:'standby'"`  // Default to standby
	// PinnedBySlug: the command that imposed a transient status. Cleared by
	// every status write that does not name one (NodeRepository.UpdateStatus).
	PinnedBySlug             string     `gorm:"column:pinned_by_slug;size:64;not null;default:''"`
	LastHeartbeat            *time.Time `gorm:"column:last_heartbeat;type:timestamp"`              // Timestamp of last heartbeat
	AgentVersion             *string    `gorm:"column:agent_version;size:50"`                      // Version of the agent software
	AgentKind                *string    `gorm:"column:agent_kind;size:20"`                         // ""/nil/"full" = managing agent; "watcher" = telemetry-only
	// LEDSources is the ambient-indication sources the node's agent advertised
	// on connect (comma-joined; NULL = none advertised, treat as the v1 pair).
	LEDSources *string `gorm:"column:led_sources;size:255"`
	AgentInstanceID          *string    `gorm:"column:agent_instance_id;size:64"`                  // instance id of the agent PROCESS last seen connecting (ConnectionInit)
	LastSeenAt               *time.Time `gorm:"column:last_seen_at;type:timestamp;index"`          // Last time node was seen (any activity)
	StorageDevices           *string    `gorm:"column:storage_devices;type:jsonb"`                 // Nullable JSON storage for storage devices map (kept for backward compatibility)
	SystemInfo               *string    `gorm:"column:system_info;type:jsonb"`                     // Nullable JSON storage for system info (network, hardware, OS, storage)
	WatchdogConfig           *string    `gorm:"column:watchdog_config;type:jsonb"`                 // Desired watchdog supervision (nil = never configured)
	LEDConfig                *string    `gorm:"column:led_config;type:jsonb"`                      // Desired LED behaviour (nil = never configured)
	LEDAppliedHash           string     `gorm:"column:led_applied_hash;size:64"`                   // LED hash the device last confirmed
	WatchdogAppliedHash      string     `gorm:"column:watchdog_applied_hash;size:64"`              // Hash the device last confirmed
	WatchdogDisarmReason     string     `gorm:"column:watchdog_disarm_reason;type:text"`           // Set when the agent's local breaker disarmed it
	AccessAppliedGeneration  string     `gorm:"column:access_applied_generation;size:64"`          // Access hash the device confirmed fully applied
	AccessAppliedAt          *time.Time `gorm:"column:access_applied_at"`                          // When it confirmed it
	WatchdogLastRefusal      string     `gorm:"column:watchdog_last_refusal;type:text"`            // Agent's pre-arm verification refusal ("<code>: <details>")
	DeviceProfile            *string    `gorm:"column:device_profile;type:jsonb"`                  // Declared hardware equipage (nil = unassigned)
	DeviceProfileAppliedHash string     `gorm:"column:device_profile_applied_hash;size:64"`        // Render hash the device last confirmed applied
	DeviceProfileScan        *string    `gorm:"column:device_profile_scan;type:jsonb"`             // Last parsed scan result (paths + HAT fields)
	DeviceProfileAppliedAt   *time.Time `gorm:"column:device_profile_applied_at"`                  // When that hash was recorded (server clock); with LastBootAt it answers whether a reboot is still owed
	LastBootAt               *time.Time `gorm:"column:last_boot_at"`                               // When the node last booted (server clock, from the agent's uptime — NOT the device's wall clock, which a blank RTC puts in 2000)
	BootOrder                *string    `gorm:"column:boot_order;type:jsonb"`                      // Boot order as JSON array of strings like ["nvme", "sd", "usb", "restart"]
	SSHKeyID                 *uuid.UUID `gorm:"column:ssh_key_id;type:uuid"`                       // SSH key ID for provisioning (nullable)
	DefaultUsername          string     `gorm:"column:default_username;size:255"`                  // Default username for provisioning
	DefaultPassword          string     `gorm:"column:default_password;type:text"`                 // Default password for provisioning: ENCRYPTED at rest (see DefaultPasswordEnc), and never returned in API responses
	// DefaultPasswordEnc marks how DefaultPassword is stored: 0 = legacy
	// plaintext, 1 = AES-GCM. A column, not a byte prefix — every key writes the
	// same "v1:" marker, so the prefix says nothing about the key, and a legacy
	// plaintext password that happens to start with it would be misread as
	// ciphertext. Same reasoning as node_files.content_enc.
	DefaultPasswordEnc    int        `gorm:"column:default_password_enc;not null;default:0"`
	Notes                 string     `gorm:"column:notes;type:text;not null;default:''"`                  // Free-form operator notes (see node.Node.Notes)
	OUID                  *uuid.UUID `gorm:"column:ou_id;type:uuid;index"`                                // Organizational unit membership (nullable)
	NetworkFirst          *bool      `gorm:"column:network_first"`                                        // Policy override (nil = inherit)
	BootToOS              *bool      `gorm:"column:boot_to_os"`                                           // Policy override (nil = inherit)
	SSHPasswordAuth       *bool      `gorm:"column:ssh_password_auth"`                                    // Policy override (nil = inherit)
	RecoveryRevertPending bool       `gorm:"column:recovery_revert_pending;not null;default:false"`       // One-shot netboot in flight
	AwaitingReturnAfter   *string    `gorm:"column:awaiting_return_after;size:64"`                        // slug the platform expects the node back from
	AwaitingReturnSince   *time.Time `gorm:"column:awaiting_return_since"`
	RecoveryPrevBootToOS  string     `gorm:"column:recovery_prev_boot_to_os;size:16;not null;default:''"` // Value to restore
	BootloaderVersion     *string    `gorm:"column:bootloader_version;size:64"`                           // EEPROM firmware build hash from ROM UA
	BootloaderTimestamp   *time.Time `gorm:"column:bootloader_timestamp;type:timestamp"`                  // EEPROM firmware build time
	BootloaderFlashedAt   *time.Time `gorm:"column:bootloader_flashed_at"`                                // When the last successful EEPROM flash settled (server clock); with LastBootAt it answers whether the reboot is still owed
	OSImageID             *uuid.UUID `gorm:"column:os_image_id;type:uuid"`                                // OS image override (nil = inherit)

	// Boot-loop failsafe (see node.Node): counter of consecutive withheld netboot
	// attempts, the timestamp anchor, the "served recovery" flag, and a per-node
	// threshold override.
	BootAttemptCount      int        `gorm:"column:boot_attempt_count;not null;default:0"`
	LastBootAttemptAt     *time.Time `gorm:"column:last_boot_attempt_at;type:timestamp"`
	BootFailsafeAt        *time.Time `gorm:"column:boot_failsafe_at;type:timestamp"`
	BootFailsafeThreshold *int       `gorm:"column:boot_failsafe_threshold"` // nil = use global default; 0 = disabled

	// Master token authentication fields
	ApprovalStatus string     `gorm:"column:approval_status;size:50;not null;default:'approved'"` // Approval status (approved, pending_approval, rejected)
	MasterTokenID  *uuid.UUID `gorm:"column:master_token_id;type:uuid;index"`                     // Master token ID (nullable) - presence indicates master token auth

	// One-shot: flash a network-first EEPROM on the first system-info report.
	AutoProvisionEEPROMPending bool `gorm:"column:auto_provision_eeprom_pending;not null;default:false"`
	// WebUSB-flashed node awaiting its first agent connect.
	AwaitingFirstConnect bool `gorm:"column:awaiting_first_connect;not null;default:false"`

	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for this model
func (NodeModel) TableName() string {
	return "nodes"
}

// ToDomain converts the SQL model to a domain model
func (m *NodeModel) ToDomain() *node.Node {
	// Handle nullable string fields
	var hostname string
	if m.Hostname != nil {
		hostname = *m.Hostname
	}

	var ipv4 string
	if m.IPv4 != nil {
		ipv4 = *m.IPv4
	}

	var macAddress string
	if m.MacAddress != nil {
		macAddress = *m.MacAddress
	}

	// Parse fingerprint from JSON - return nil if empty
	var fingerprint *node.HardwareFingerprint
	if m.Fingerprint != nil && *m.Fingerprint != "" {
		var fp node.HardwareFingerprint
		if err := json.Unmarshal([]byte(*m.Fingerprint), &fp); err == nil {
			fingerprint = &fp
		}
	}

	// Handle agent version
	var agentVersion string
	if m.AgentVersion != nil {
		agentVersion = *m.AgentVersion
	}
	var agentKind string
	if m.AgentKind != nil {
		agentKind = *m.AgentKind
	}
	agentInstanceID := ""
	if m.AgentInstanceID != nil {
		agentInstanceID = *m.AgentInstanceID
	}
	var ledSources []string
	if m.LEDSources != nil && *m.LEDSources != "" {
		ledSources = strings.Split(*m.LEDSources, ",")
	}

	// Parse storage devices from JSON - return empty map if nil or empty
	storageDevices := make(map[string]*node.StorageDevice)
	if m.StorageDevices != nil && *m.StorageDevices != "" {
		if err := json.Unmarshal([]byte(*m.StorageDevices), &storageDevices); err != nil {
			// If unmarshal fails, use empty map
			storageDevices = make(map[string]*node.StorageDevice)
		}
	}

	// Parse system info from JSON - return nil if empty
	var systemInfo *node.SystemInfo
	if m.SystemInfo != nil && *m.SystemInfo != "" {
		var si node.SystemInfo
		if err := json.Unmarshal([]byte(*m.SystemInfo), &si); err == nil {
			systemInfo = &si
		}
	}

	// Parse boot order from JSON - return empty array if nil or empty
	var bootOrder []string
	if m.BootOrder != nil && *m.BootOrder != "" {
		if err := json.Unmarshal([]byte(*m.BootOrder), &bootOrder); err != nil {
			// If unmarshal fails, use empty array
			bootOrder = []string{}
		}
	}

	var bootloaderVersion string
	if m.BootloaderVersion != nil {
		bootloaderVersion = *m.BootloaderVersion
	}

	var ledConfig *node.LEDConfig
	if m.LEDConfig != nil && *m.LEDConfig != "" {
		var lc node.LEDConfig
		if err := json.Unmarshal([]byte(*m.LEDConfig), &lc); err == nil {
			ledConfig = &lc
		}
	}
	var watchdogConfig *node.WatchdogConfig
	if m.WatchdogConfig != nil && *m.WatchdogConfig != "" {
		var wc node.WatchdogConfig
		if err := json.Unmarshal([]byte(*m.WatchdogConfig), &wc); err == nil {
			watchdogConfig = &wc
		}
	}

	var deviceProfile *device_profile.Equipage
	if m.DeviceProfile != nil && *m.DeviceProfile != "" {
		var eq device_profile.Equipage
		if err := json.Unmarshal([]byte(*m.DeviceProfile), &eq); err == nil && !eq.IsZero() {
			deviceProfile = &eq
		}
	}
	var deviceProfileScan *device_profile.ScanResult
	if m.DeviceProfileScan != nil && *m.DeviceProfileScan != "" {
		var sr device_profile.ScanResult
		if err := json.Unmarshal([]byte(*m.DeviceProfileScan), &sr); err == nil {
			deviceProfileScan = &sr
		}
	}

	return &node.Node{
		ID:                   m.ID,
		AccountID:            m.AccountID,
		Name:                 m.Name,
		Hostname:             hostname,
		IPv4:                 ipv4,
		MacAddress:           macAddress,
		Fingerprint:          fingerprint,
		UniqueToken:          m.UniqueToken,
		Status:               node.NodeStatus(m.Status),
		PinnedBySlug:         m.PinnedBySlug,
		LastHeartbeat:        m.LastHeartbeat,
		AgentVersion:         agentVersion,
		AgentKind:            agentKind,
		AgentInstanceID:      agentInstanceID,
		LEDSources:           ledSources,
		LastSeenAt:           m.LastSeenAt,
		StorageDevices:       storageDevices,
		SystemInfo:           systemInfo,
		WatchdogConfig:       watchdogConfig,
		WatchdogAppliedHash:  m.WatchdogAppliedHash,
		LEDConfig:            ledConfig,
		LEDAppliedHash:       m.LEDAppliedHash,
		WatchdogDisarmReason: m.WatchdogDisarmReason,
		WatchdogLastRefusal:  m.WatchdogLastRefusal,

		DeviceProfile:            deviceProfile,
		DeviceProfileAppliedHash: m.DeviceProfileAppliedHash,
		DeviceProfileAppliedAt:   m.DeviceProfileAppliedAt,
		LastBootAt:               m.LastBootAt,
		DeviceProfileScan:        deviceProfileScan,

		AccessAppliedGeneration: m.AccessAppliedGeneration,
		AccessAppliedAt:         m.AccessAppliedAt,
		BootOrder:               bootOrder,
		SSHKeyID:                m.SSHKeyID,
		DefaultUsername:         m.DefaultUsername,
		DefaultPassword:         m.DefaultPassword,
		Notes:                   m.Notes,
		OUID:                    m.OUID,
		NetworkFirst:            m.NetworkFirst,
		BootToOS:                m.BootToOS,
		SSHPasswordAuth:         m.SSHPasswordAuth,
		RecoveryRevertPending:   m.RecoveryRevertPending,
		AwaitingReturnAfter:     strFromPtr(m.AwaitingReturnAfter),
		AwaitingReturnSince:     m.AwaitingReturnSince,
		RecoveryPrevBootToOS:    m.RecoveryPrevBootToOS,
		BootloaderVersion:       bootloaderVersion,
		BootloaderTimestamp:     m.BootloaderTimestamp,
		BootloaderFlashedAt:     m.BootloaderFlashedAt,
		BootAttemptCount:        m.BootAttemptCount,
		LastBootAttemptAt:       m.LastBootAttemptAt,
		BootFailsafeAt:          m.BootFailsafeAt,
		BootFailsafeThreshold:   m.BootFailsafeThreshold,
		OSImageID:               m.OSImageID,
		ApprovalStatus:          node.ApprovalStatus(m.ApprovalStatus),
		MasterTokenID:           m.MasterTokenID,

		AutoProvisionEEPROMPending: m.AutoProvisionEEPROMPending,
		AwaitingFirstConnect:       m.AwaitingFirstConnect,

		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
	}
}

// FromDomain updates the SQL model from a domain model
func (m *NodeModel) FromDomain(n *node.Node) {
	m.ID = n.ID
	m.AccountID = n.AccountID
	m.Name = n.Name

	// Handle nullable fields - set to nil if empty
	if n.Hostname != "" {
		m.Hostname = &n.Hostname
	} else {
		m.Hostname = nil
	}

	if n.IPv4 != "" {
		m.IPv4 = &n.IPv4
	} else {
		m.IPv4 = nil
	}

	if n.MacAddress != "" {
		m.MacAddress = &n.MacAddress
	} else {
		m.MacAddress = nil
	}

	if n.LEDConfig != nil {
		ledJSON, _ := json.Marshal(n.LEDConfig)
		raw := string(ledJSON)
		m.LEDConfig = &raw
	} else {
		m.LEDConfig = nil
	}
	m.LEDAppliedHash = n.LEDAppliedHash
	if n.WatchdogConfig != nil {
		watchdogJSON, _ := json.Marshal(n.WatchdogConfig)
		raw := string(watchdogJSON)
		m.WatchdogConfig = &raw
	} else {
		m.WatchdogConfig = nil
	}
	m.WatchdogAppliedHash = n.WatchdogAppliedHash
	m.WatchdogDisarmReason = n.WatchdogDisarmReason
	m.WatchdogLastRefusal = n.WatchdogLastRefusal
	m.AccessAppliedGeneration = n.AccessAppliedGeneration
	m.AccessAppliedAt = n.AccessAppliedAt

	if n.DeviceProfile != nil && !n.DeviceProfile.IsZero() {
		profileJSON, _ := json.Marshal(n.DeviceProfile)
		raw := string(profileJSON)
		m.DeviceProfile = &raw
	} else {
		m.DeviceProfile = nil
	}
	m.DeviceProfileAppliedHash = n.DeviceProfileAppliedHash
	m.DeviceProfileAppliedAt = n.DeviceProfileAppliedAt
	m.LastBootAt = n.LastBootAt
	if n.DeviceProfileScan != nil {
		scanJSON, _ := json.Marshal(n.DeviceProfileScan)
		raw := string(scanJSON)
		m.DeviceProfileScan = &raw
	} else {
		m.DeviceProfileScan = nil
	}

	// Serialize fingerprint to JSON - set to nil if fingerprint is nil
	if n.Fingerprint != nil {
		fingerprintJSON, _ := json.Marshal(n.Fingerprint)
		fingerprintStr := string(fingerprintJSON)
		m.Fingerprint = &fingerprintStr
	} else {
		m.Fingerprint = nil
	}

	m.UniqueToken = n.UniqueToken
	m.Status = string(n.Status)
	m.PinnedBySlug = n.PinnedBySlug
	m.LastHeartbeat = n.LastHeartbeat
	m.LastSeenAt = n.LastSeenAt
	m.CreatedAt = n.CreatedAt
	m.UpdatedAt = n.UpdatedAt

	// Handle agent version - set to nil if empty
	if n.AgentVersion != "" {
		m.AgentVersion = &n.AgentVersion
	} else {
		m.AgentVersion = nil
	}
	if n.AgentKind != "" {
		m.AgentKind = &n.AgentKind
	} else {
		m.AgentKind = nil
	}
	if len(n.LEDSources) > 0 {
		joined := strings.Join(n.LEDSources, ",")
		m.LEDSources = &joined
	} else {
		m.LEDSources = nil
	}
	if n.AgentInstanceID != "" {
		m.AgentInstanceID = &n.AgentInstanceID
	} else {
		m.AgentInstanceID = nil
	}

	// Serialize storage devices to JSON - set to nil if empty
	if len(n.StorageDevices) > 0 {
		storageDevicesJSON, _ := json.Marshal(n.StorageDevices)
		storageDevicesStr := string(storageDevicesJSON)
		m.StorageDevices = &storageDevicesStr
	} else {
		m.StorageDevices = nil
	}

	// Serialize system info to JSON - set to nil if system info is nil
	if n.SystemInfo != nil {
		systemInfoJSON, _ := json.Marshal(n.SystemInfo)
		systemInfoStr := string(systemInfoJSON)
		m.SystemInfo = &systemInfoStr
	} else {
		m.SystemInfo = nil
	}

	// Serialize boot order to JSON - set to nil if empty
	if len(n.BootOrder) > 0 {
		bootOrderJSON, _ := json.Marshal(n.BootOrder)
		bootOrderStr := string(bootOrderJSON)
		m.BootOrder = &bootOrderStr
	} else {
		m.BootOrder = nil
	}

	m.SSHKeyID = n.SSHKeyID
	m.DefaultUsername = n.DefaultUsername
	m.DefaultPassword = n.DefaultPassword
	m.Notes = n.Notes
	m.OUID = n.OUID
	m.NetworkFirst = n.NetworkFirst
	m.BootToOS = n.BootToOS
	m.SSHPasswordAuth = n.SSHPasswordAuth
	m.RecoveryRevertPending = n.RecoveryRevertPending
	if n.AwaitingReturnAfter != "" {
		m.AwaitingReturnAfter = &n.AwaitingReturnAfter
	} else {
		m.AwaitingReturnAfter = nil
	}
	m.AwaitingReturnSince = n.AwaitingReturnSince
	m.RecoveryPrevBootToOS = n.RecoveryPrevBootToOS
	m.OSImageID = n.OSImageID

	if n.BootloaderVersion != "" {
		m.BootloaderVersion = &n.BootloaderVersion
	} else {
		m.BootloaderVersion = nil
	}
	m.BootloaderTimestamp = n.BootloaderTimestamp
	m.BootloaderFlashedAt = n.BootloaderFlashedAt
	m.BootAttemptCount = n.BootAttemptCount
	m.LastBootAttemptAt = n.LastBootAttemptAt
	m.BootFailsafeAt = n.BootFailsafeAt
	m.BootFailsafeThreshold = n.BootFailsafeThreshold

	// Master token authentication fields
	m.ApprovalStatus = string(n.ApprovalStatus)
	m.MasterTokenID = n.MasterTokenID
	m.AutoProvisionEEPROMPending = n.AutoProvisionEEPROMPending
	m.AwaitingFirstConnect = n.AwaitingFirstConnect
}

func strFromPtr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
