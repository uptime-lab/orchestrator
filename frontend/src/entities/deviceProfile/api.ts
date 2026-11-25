import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// Device profiles: declared hardware equipage (carrier board + factory
// variant + plugged modules). The declaration is manual — a carrier board is
// not detectable — the platform applies the required config.txt overlays, and
// a scan afterwards only CONFIRMS what was declared. Plain api functions per
// the entities convention; polling useQuery lives in the widgets.

export type DeviceProfileOptionDef = {
  id: string
  name: string
  description?: string
  overlays?: string[]
  capabilities?: string[]
  has_probe: boolean
}

export type DeviceProfileVariant = {
  id: string
  name: string
  notes?: string
  peripherals?: DeviceProfileOptionDef[]
}

export type DeviceProfilePort = {
  id: string
  name: string
  pinout: string
  pin_from: number
  pin_to: number
  shared_buses?: string[]
}

export type DeviceProfileBoard = {
  id: string
  name: string
  description?: string
  fits?: string[]
  variants?: DeviceProfileVariant[]
  fixed?: DeviceProfileOptionDef[]
  ports?: DeviceProfilePort[]
  // module id -> the board's first port carrying every function the module
  // needs. Computed server-side (the pinout dialect has one implementation);
  // the UI filters module options by it.
  compatible_modules?: Record<string, string>
  // Board-level LED capability summary (absent = no LED features).
  led?: { stealth_available: boolean; digital_leds: number }
}

export type DeviceProfileModule = DeviceProfileOptionDef & {
  needs: string[]
  i2c_address?: string
}

export type DeviceProfileCatalog = {
  boards: DeviceProfileBoard[]
  modules: DeviceProfileModule[]
}

export type EquippedModule = { module_id: string; port_id: string }

// The ladder: declared -> applied -> confirmed. update_pending = applied once
// but the desired render changed since (re-apply is an explicit action);
// not_confirmed = applied but the scan did not see the hardware — the honest
// "declared, but the board does not answer" signal.
// working = confirmed AND the OS is actually using the hardware for its
// purpose (an RTC that the kernel takes the system clock from). A rung above
// confirmed: answering and being used are different facts.
export type DeviceProfileOptionStatus =
  | 'declared'
  | 'update_pending'
  | 'applied'
  | 'confirmed'
  | 'working'
  | 'not_confirmed'

export type NodeDeviceProfileOption = {
  kind: 'fixed' | 'variant' | 'module'
  id: string
  name: string
  port_id?: string
  status: DeviceProfileOptionStatus
  capabilities?: string[]
}

export type NodeDeviceProfile = {
  assigned: boolean
  source?: 'node' | 'ou' | 'root_ou' | 'account'
  profile_id?: string
  variant_id?: string
  modules?: EquippedModule[]
  options?: NodeDeviceProfileOption[]
  capabilities?: string[]
  desired_hash?: string
  applied_hash?: string
  compatible_profiles: string[]
  board_kind?: string
  hat?: Record<string, string>
  scanned_at?: string
  // The applied boot config still needs the reboot that activates it.
  // UNDEFINED means the server cannot tell (nothing applied, an edit
  // outstanding, or an agent reporting no uptime) — offer the reboot then.
  // Only an explicit false may hide it.
  reboot_pending?: boolean
}

export type DeviceProfilePreview = {
  path: string
  marker: string
  content: string
  hash: string
  setup_kinds?: string[]
  up_to_date: boolean
  reboot_required: boolean
}

export type DeviceProfileScanState = {
  status: 'running' | 'completed' | 'failed'
  detail?: string
  paths?: string[]
  hat?: Record<string, string>
}

export const getDeviceProfileCatalog =
  async (): Promise<DeviceProfileCatalog> => {
    const response = await apiClient.get<ApiResponse<DeviceProfileCatalog>>(
      '/api/v1/device-profiles',
    )
    return response.data.data
  }

export const getNodeDeviceProfile = async (
  nodeId: string,
): Promise<NodeDeviceProfile> => {
  const response = await apiClient.get<ApiResponse<NodeDeviceProfile>>(
    `/api/v1/nodes/${nodeId}/device-profile`,
  )
  return response.data.data
}

// An empty profile_id clears the node's own declaration (falls back to
// OU/account inheritance). Assignment stores only — nothing reaches the
// device until an explicit apply.
export const putNodeDeviceProfile = async (
  nodeId: string,
  equipage: {
    profile_id: string
    variant_id?: string
    modules?: EquippedModule[]
  },
): Promise<NodeDeviceProfile> => {
  const response = await apiClient.put<ApiResponse<NodeDeviceProfile>>(
    `/api/v1/nodes/${nodeId}/device-profile`,
    equipage,
  )
  return response.data.data
}

export const getDeviceProfilePreview = async (
  nodeId: string,
): Promise<DeviceProfilePreview> => {
  const response = await apiClient.get<ApiResponse<DeviceProfilePreview>>(
    `/api/v1/nodes/${nodeId}/device-profile/preview`,
  )
  return response.data.data
}

// Dispatches ONE queueable, managed-OS-only command; an offline node queues it
// (queued=true, no command_id) for the next managed-OS connect.
export const applyDeviceProfile = async (
  nodeId: string,
): Promise<{ command_id?: string; queued: boolean }> => {
  const response = await apiClient.post<
    ApiResponse<{ command_id?: string; queued: boolean }>
  >(`/api/v1/nodes/${nodeId}/device-profile/apply`)
  return response.data.data
}

export const scanDeviceProfile = async (nodeId: string): Promise<string> => {
  const response = await apiClient.post<ApiResponse<{ command_id: string }>>(
    `/api/v1/nodes/${nodeId}/device-profile/scan`,
  )
  return response.data.data.command_id
}

export const getDeviceProfileScan = async (
  nodeId: string,
  commandId: string,
): Promise<DeviceProfileScanState> => {
  const response = await apiClient.get<ApiResponse<DeviceProfileScanState>>(
    `/api/v1/nodes/${nodeId}/device-profile/scan/${commandId}`,
  )
  return response.data.data
}

// Blink the locate LED (sysfs ACT everywhere; boards with digital LEDs blink
// those too). stop=true ends a running blink — a real stop since the agent
// gained one; an older agent renders it as the legacy 5-second minimum blink.
// Returns the EFFECTIVE duration alongside the command id — the account
// setting decides it server-side, and the web lamp must blink exactly as long
// as the device, so the caller times the lamp from this, never a constant.
export const identifyNode = async (
  nodeId: string,
  opts?: { durationSec?: number; stop?: boolean },
): Promise<{ command_id: string; duration_sec: number }> => {
  const body: { duration_sec?: number; stop?: boolean } = {}
  if (opts?.durationSec) body.duration_sec = opts.durationSec
  if (opts?.stop) body.stop = true
  const response = await apiClient.post<
    ApiResponse<{ command_id: string; duration_sec: number }>
  >(`/api/v1/nodes/${nodeId}/identify`, body)
  return response.data.data
}

// The transient identify/button registry — what a freshly opened page
// hydrates the blink lamps and press marks from (the walk-to-the-PC case:
// press the button at the rack, sit down, open the site, find the node).
export type NodeIdentifyState = {
  node_id: string
  blink_until?: string
  pressed_at?: string
}

export const fetchIdentifyStates = async (): Promise<NodeIdentifyState[]> => {
  const response = await apiClient.get<ApiResponse<NodeIdentifyState[]>>(
    '/api/v1/nodes/identify-state',
  )
  return response.data.data ?? []
}
