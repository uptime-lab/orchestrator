import type { Flag } from '@/entities/ou/api/types'

// Boot device types - now supports all boot options (string for flexibility)
export type BootDevice = string
export type BootOrder = BootDevice[]

// Boot option from API
export type BootOption = {
  value: string
  label: string
  hex_digit: string
  is_default: boolean
  description: string
}

// Node status values from backend (matching backend/pkg/webapp/domain/node/node.go)
export type NodeStatus =
  | 'ready_for_adoption'
  | 'flashing'
  | 'maintenance'
  | 'online'
  | 'offline'
  | 'restarting'
  | 'shutdown'
  | 'standby'
  | 'unreachable'
  | 'adopting'
  | 'adoption_failed'
  | 'shutting_down'
  | 'busy'
  | 'os_installing'
  | 'os_installation_failed'

// Status constants matching backend (backend/pkg/webapp/domain/node/node.go)
export const NodeStatusReadyForAdoption: NodeStatus = 'ready_for_adoption'

// Legacy enum for backward compatibility - deprecated, use NodeStatus string type instead
export enum BladeStatus {
  Unreachable = 0,
  Offline = 1,
  Online = 2,
  Standby = 3,
  ReadyForAdoption = 10,
  Adopting = 11,
  AdoptionFailed = 12,
}

export enum NodeRole {
  Primary = 1,
  Secondary = 2,
}

export type NodeResourceType = 'cpu' | 'mem' | 'net' | 'temp' | 'disk'

export type NodeResource = {
  usage: number | string | undefined
  type: NodeResourceType
  name?: string | undefined
}

// API response types matching swagger
export type HardwareFingerprint = {
  mac_address: string
  hardware_serial?: string
}

export type StoragePartition = {
  partition_id: string
  mount_point: string
  filesystem: string
  used_space: number
  available_space: number
}

export type StorageDevice = {
  path: string
  label: string
  model: string
  vendor: string
  serial: string
  device_type: string
  total_capacity: number
  is_removable: boolean
  is_read_only: boolean
  partitions: StoragePartition[]
}

export type NetworkInfo = {
  ip: string
  hostname: string
}

export type CPUInfo = {
  model: string
  vendor: string
  cores: number
  info: string
  // Raw Raspberry Pi board revision code (e.g. "c04170"). Absent on non-Pi
  // hardware and on agents older than 0.4.5. Prefer it over `model` for
  // anything board-specific — see entities/node/board.ts.
  board_revision?: string
}

export type RAMInfo = {
  size: number
}

export type TPMInfo = {
  present: boolean
  version: string
  model: string
  description: string
}

export type EEPROMInfo = {
  enabled: boolean
  description?: string
}

export type BootInfo = {
  current_boot_device?: string
  boot_devices_order?: BootOrder
  booted_at?: number // Unix timestamp (seconds since epoch) when the system booted
  uptime_seconds?: number // Seconds since boot (clock-independent; reliable on RTC-less/netbooted devices)
  boot_http_host?: string // EEPROM-baked netboot server (HTTP_HOST) this device fetches from
  // True while the node runs the RAM-backed recovery image. current_boot_device
  // cannot answer this: the same bootstrap reports "http" when netbooted and
  // "sd" when booted from a provisioning card.
  recovery_bootstrap?: boolean
  // Whole disk the node is running from ("/dev/nvme0n1"). Absent on agents
  // older than 0.5.5, and absent in the recovery bootstrap — where the root is
  // RAM, so no local device booted this node. That absence is a fact, not a
  // gap: see resolveBootDevicePath.
  root_device?: string
  // How the root filesystem keeps writes: "persistent", "overlay" (overlayroot
  // — writes land in a tmpfs upper layer and evaporate at reboot; the platform
  // refuses write-shaped commands with 409 overlay_root), or absent from an
  // agent older than 0.7.0 or a RAM-backed recovery root.
  root_persistence?: 'persistent' | 'overlay' | string
}

// PowerInfo is the decoded `vcgencmd get_throttled` mask: what the SoC is doing
// about power and heat now, and what it has done since boot (sticky until the
// node reboots).
//
// ABSENT — not zeroed — when the device cannot answer: not a Pi, no vcgencmd, or
// an agent older than 0.5.4. Treat `power === undefined` as "unknown" and never
// as "healthy": the whole point is that under-voltage explains failures nothing
// else on a node can explain, so claiming a clean supply for a node that never
// reported one is the one wrong answer available here.
export type PowerInfo = {
  // Raw mask, kept so a bit this build does not decode is still visible.
  throttled_flags: number
  // Right now.
  under_voltage_now?: boolean
  freq_capped_now?: boolean
  throttled_now?: boolean
  soft_temp_limit_now?: boolean
  // Since boot — sticky, and the half that explains a failure after the fact.
  under_voltage_since_boot?: boolean
  freq_capped_since_boot?: boolean
  throttled_since_boot?: boolean
  soft_temp_limit_since_boot?: boolean
}

// USBDevice is one thing plugged into a USB port. Hubs and mass storage are
// filtered out on the device: a hub is structure rather than an attachment, and
// a USB disk is already a StorageDevice — two records for one object invite an
// operator to reason about them separately.
export type USBDevice = {
  vendor_id: string
  product_id: string
  manufacturer?: string
  product?: string
  class_code: number
  speed?: string // Mbit/s as the kernel reports it
  port?: string // bus path; what tells two identical dongles apart
}

// HATInfo is a spec-compliant HAT's ID EEPROM identity. Absent = no HAT
// present (or an agent older than 0.6.0 — nobody looked).
export type HATInfo = {
  vendor?: string
  product?: string
  product_id?: string
  product_ver?: string
  uuid?: string
}

export type HardwareInfo = {
  cpu?: CPUInfo
  ram?: RAMInfo
  tpm?: TPMInfo
  boot?: BootInfo
  eeprom?: EEPROMInfo
  power?: PowerInfo
  // Absent on agents older than 0.5.5 and on hardware with no USB bus. Absent
  // means "nobody looked", not "nothing is attached".
  usb?: USBDevice[]
  hat?: HATInfo
}

export type OSInfo = {
  version: string
  details: string
}

export type SystemInfo = {
  network?: NetworkInfo
  hardware?: HardwareInfo
  os?: OSInfo
  storage_devices?: Record<string, StorageDevice>
}

// Approval status for master token authentication
export type ApprovalStatus = 'approved' | 'pending_approval' | 'rejected'

// Authentication mode
export type AuthMode = 'node_token' | 'master_token'

export type NodeResult = {
  id: string
  name: string
  hostname: string
  ipv4: string
  mac_address: string
  status: NodeStatus
  account_id: string
  created_at: string
  updated_at: string
  fingerprint?: HardwareFingerprint
  storage_devices?: Record<string, StorageDevice>
  system_info?: SystemInfo
  boot_order?: BootOrder
  ssh_key_id?: string // SSH key ID for provisioning (never password)
  default_username?: string // Default username for provisioning
  notes?: string // Free-form operator notes (searchable from the fleet list)
  agent_version?: string // Agent software version last reported on connect
  agent_kind?: 'full' | 'watcher' // watcher = telemetry-only agent; the platform refuses commands
  last_seen_at?: number // Unix timestamp (seconds since epoch) of last activity
  // When the node last booted, on the SERVER's clock, from the agent's uptime.
  // A CHANGE here means the node rebooted — not merely reconnected — which is
  // the only reliable trigger for refreshing anything that depends on a boot.
  last_boot_at?: number // Unix timestamp (seconds since epoch)
  bootloader_version?: string // EEPROM firmware build hash from the ROM's last HTTP-boot request
  bootloader_timestamp?: number // EEPROM firmware build time (unix seconds)
  bootloader_flashed_at?: number // when the last successful EEPROM flash settled (unix seconds, server clock)
  // The command that imposed a transient status, when one did. "restarting" is
  // a claim the platform makes at dispatch for any command that might take the
  // device down; this says which, so a flash that keeps the node up is not
  // announced as a reboot.
  pinned_by_slug?: string
  bootloader_reboot_pending?: boolean // flashed firmware still awaits the boot that activates it (absent = unknown)
  // Master token authentication fields
  approval_status: ApprovalStatus
  auth_mode: AuthMode
  master_token_id?: string
  processor_serial?: string
  // Organizational unit assignment and per-node policy overrides
  ou_id?: string
  network_first: Flag
  boot_to_os: Flag
  ssh_password_auth: Flag
  // One-shot reboot-to-recovery in flight (netboot once, then restore)
  recovery_revert_pending?: boolean
  // Set while the platform expects the node back (successful install_os, a
  // reboot the unreachable sweeper gave up on). Offline + this = the node
  // never came back after that command.
  awaiting_return_after?: string
  awaiting_return_since?: string
  // WebUSB-flashed node: approved, waiting to reboot/netboot/connect the 1st time
  awaiting_first_connect?: boolean
  // Boot-loop failsafe: set (unix seconds) when the platform served recovery after
  // repeated withheld netboots (a crash loop); drives the "auto-recovered" badge
  // until a healthy OS boot or an operator acknowledge clears it.
  boot_failsafe_at?: number
  boot_attempt_count?: number
  boot_failsafe_threshold?: number
  // Watchdog: the device's own breaker took its watchdog down after repeated
  // reboots. The node is stable but no longer supervised, which is only visible
  // in the fleet list if the list says so. Cleared by the next confirmed apply.
  watchdog_disarmed?: boolean
  watchdog_disarm_reason?: string
  // default_password is intentionally NOT included - never returned in API responses
}

export type NodeListResult = {
  nodes: NodeResult[]
  count: number
}

// The paginated envelope GET /nodes serves when page/per_page is present.
export type NodePageResult = {
  nodes: NodeResult[]
  total: number
  page: number
  per_page: number
}

// A live resource-utilization sample pushed over WebSocket (node_metrics).
export type NodeThermalZone = {
  name: string
  temp_celsius: number
}

export type NodeMetricsSnapshot = {
  timestamp: number
  cpu_percent: number
  cpu_percent_per_core?: number[]
  mem_total_bytes: number
  mem_used_bytes: number
  mem_available_bytes: number
  load_avg_1: number
  load_avg_5: number
  load_avg_15: number
  thermal?: NodeThermalZone[]
}

export type NodeMetricsEvent = {
  node_id: string
  metrics: NodeMetricsSnapshot
}

// Extended type for UI compatibility (adds optional fields not in API)
export type NodeData = NodeResult & {
  role?: NodeRole
  tags?: string[]
  resources?: NodeResource[]
  cluster?: string
}

// File status values
export type FileStatus = 'saved' | 'applying' | 'applied' | 'failed'

// Node file type
export type NodeFile = {
  id?: string
  path: string
  content?: string
  version: number
  status: FileStatus
  hash: string
  created_at: string
  error_message?: string
}

// TrackedFile is one row of a node's Files list: the tracked record plus the
// latest version's metadata. Never content — that is a separate, higher-privilege
// read.
export type TrackedFile = {
  path: string
  source: 'platform' | 'user'
  protected: boolean
  never_read: boolean
  can_apply: boolean
  mode?: string
  owner?: string
  group?: string
  size: number
  last_read_at?: number
  last_applied_at?: number
  version?: number
  status?: FileStatus
  hash?: string
  updated_at?: string
}

// TrackedFileRead is the state of an in-flight track/refresh read.
export type TrackedFileRead = {
  status: 'running' | 'completed' | 'failed'
  detail?: string
  path?: string
  size?: number
  mode?: string
  owner?: string
  group?: string
  truncated?: boolean
  version?: number
  changed?: boolean
}

// Node file list response
export type NodeFileListResult = {
  files: NodeFile[]
}

// Node file versions response
export type NodeFileVersionsResult = {
  versions: NodeFile[]
}

// Legacy type aliases for backward compatibility (config files are an edge case)
export type ConfigFileStatus = FileStatus
export type NodeConfigFile = NodeFile
export type NodeConfigFileListResult = NodeFileListResult
export type NodeConfigFileVersionsResult = NodeFileVersionsResult

// Node command types
// The three beyond success/failed are deliberate non-failures: a queued command
// replaced by a newer one for the same slug (superseded), one whose expires_at
// passed before the node came back (expired), and one a precondition refused on
// arrival — in practice an agent below the slug's floor (skipped).
export type NodeCommandStatus =
  | 'registered'
  | 'sent'
  | 'in_progress'
  | 'success'
  | 'failed'
  | 'superseded'
  | 'expired'
  | 'skipped'

export type NodeCommand = {
  id: string
  node_id: string
  slug: string
  payload: Record<string, unknown>
  status: NodeCommandStatus
  progress: number // 0-100
  details: string
  // Structured outputs the agent reported (run_script exit_code/stdout/stderr,
  // read_file content). Values are placeholder-redacted for callers without
  // secret.read_value; the keys always survive, so an empty map and a withheld
  // one are distinguishable.
  result?: Record<string, string>
  workflow_execution_node_id?: string
  created_at: string
  updated_at: string
}

export type NodeCommandListResult = {
  commands: NodeCommand[]
  limit: number
  offset: number
}
