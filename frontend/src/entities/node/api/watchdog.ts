import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// Watchdog supervision. The platform stores a desired configuration and
// dispatches a converge command; the device confirms what it actually armed,
// and can disarm itself if a config turns out to reboot-loop it. So the UI
// always shows two facts, not one: what was asked for and what is running.

export type WatchdogPreset = 'agent-health' | 'os-hang' | 'network' | 'full'

export type WatchdogConfig = {
  enabled: boolean
  os_hang: boolean
  watchdog_timeout_sec: number
  network: boolean
  ping_targets?: string[]
  ping_count?: number
  interface?: string
  agent_health: boolean
  agent_change_sec?: number
  max_load_1?: number
  max_load_5?: number
  max_load_15?: number
  min_memory_pages?: number
  max_temperature_c?: number
  interval_sec?: number
  retry_timeout_sec?: number
}

// The apply command the platform last dispatched for this node's watchdog.
// Status is a NodeCommandStatus-like string ('registered'/'sent'/'in_progress'
// are in flight; 'failed'/'expired'/'cancelled' are terminal failures) — kept
// as a plain string so a new backend status cannot break the panel.
export type WatchdogCommandRef = {
  id: string
  status: string
  details?: string
}

export type WatchdogState = {
  desired?: WatchdogConfig
  desired_hash?: string
  // Which level the desired config came from ('node'|'ou'|'root_ou'|'account').
  // An inherited config is shown with its source; applying it stays an
  // explicit per-node action.
  desired_source?: string
  applied_hash?: string
  in_sync: boolean
  disarm_reason?: string
  public_ping_targets?: string[]
  // Only ever true for an enabled desired config.
  recovery_limited?: boolean
  // The apply command was accepted for an offline node and waits for reconnect
  // — nothing has been applied yet.
  queued: boolean
  // "<code>: <details>" from the device's pre-arm safety check refusing the
  // last apply. Cleared by the backend when a later apply succeeds.
  last_refusal?: string
  last_command?: WatchdogCommandRef
}

export const getWatchdog = async (nodeId: string): Promise<WatchdogState> => {
  const response = await apiClient.get<ApiResponse<WatchdogState>>(
    `/api/v1/nodes/${nodeId}/watchdog`,
  )
  return response.data.data
}

export const putWatchdog = async (
  nodeId: string,
  body: { preset?: WatchdogPreset; ping_target?: string } | WatchdogConfig,
): Promise<WatchdogState> => {
  const response = await apiClient.put<ApiResponse<WatchdogState>>(
    `/api/v1/nodes/${nodeId}/watchdog`,
    body,
  )
  return response.data.data
}

export const disarmWatchdog = async (
  nodeId: string,
): Promise<WatchdogState> => {
  const response = await apiClient.delete<ApiResponse<WatchdogState>>(
    `/api/v1/nodes/${nodeId}/watchdog`,
  )
  return response.data.data
}

// The preset catalog, including the wording a confirmation must show. It comes
// from the server so the four presets — and above all the sentence describing
// what the device will do once armed — are defined once, next to the bounds
// that enforce them, instead of drifting in a copy kept here.
export type WatchdogPresetInfo = {
  name: WatchdogPreset
  label: string
  description: string
  // Contains WATCHDOG_TARGET_TOKEN when requires_ping_target is set: the
  // sentence is only true of a specific address.
  consequence: string
  requires_ping_target: boolean
  config: WatchdogConfig
}

// The placeholder the network preset's consequence carries for the address the
// operator names. Mirrors nodeDomain.WatchdogTargetToken.
export const WATCHDOG_TARGET_TOKEN = '{{target}}'

export const getWatchdogPresets = async (
  nodeId: string,
): Promise<WatchdogPresetInfo[]> => {
  const response = await apiClient.get<
    ApiResponse<{ presets: WatchdogPresetInfo[] }>
  >(`/api/v1/nodes/${nodeId}/watchdog-presets`)
  return response.data.data.presets
}

// A live probe of the device. Everything in WatchdogState is a memory of the
// last converge command; this reads the node itself, which is the only way to
// see a watchdog someone stopped by hand on the device.
export type WatchdogStatusScan = {
  status: 'running' | 'completed' | 'failed'
  detail?: string
  // systemd's own words. Empty means the probe could not tell — never read an
  // empty answer as "inactive".
  service_state?: string
  service_enabled?: string
  device_present: boolean
  conf_present: boolean
  drop_in_present: boolean
  breaker_known: boolean
  applied_hash?: string
  breaker_boot_id?: string
  breaker_boots?: number
  breaker_disarmed: boolean
  // The benign counterpart: supervision is off because the platform asked, not
  // because the device's breaker caught anything.
  disarmed_by_platform?: boolean
  breaker_reason?: string
  liveness_age_sec?: number
}

export const scanWatchdogStatus = async (nodeId: string): Promise<string> => {
  const response = await apiClient.post<ApiResponse<{ command_id: string }>>(
    `/api/v1/nodes/${nodeId}/watchdog/status-scan`,
  )
  return response.data.data.command_id
}

export const getWatchdogStatusScan = async (
  nodeId: string,
  commandId: string,
): Promise<WatchdogStatusScan> => {
  const response = await apiClient.get<ApiResponse<WatchdogStatusScan>>(
    `/api/v1/nodes/${nodeId}/watchdog/status-scan/${commandId}`,
  )
  return response.data.data
}
