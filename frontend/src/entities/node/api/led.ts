import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// LED behaviour: stealth mode (blade transistor cuts every LED except the
// digital chain), ACT/PWR disable (any Pi), and per-LED ambient indication
// (temperature / load) on the digital chain. The platform stores a desired
// config and dispatches a converge; the agent persists it and re-applies it
// on boot, so the UI shows desired vs applied like the watchdog panel does.

export type LEDSource = 'off' | 'temperature' | 'load' | 'ram' | 'disk'

// LED v2 per-LED parameterization. Zero (or omitted) low/high/brightness mean
// "the per-source default" — 45..80 °C for temperature, 0..100 % for the
// percentage sources, 20 % brightness — matching the backend's DigitalLEDSpec.
export type DigitalLEDSpec = {
  source: LEDSource
  low?: number
  high?: number
  brightness?: number
}

export type LEDConfig = {
  stealth_mode: boolean
  act_led_off: boolean
  pwr_led_off: boolean
  // v1 form: sources only, agent-side default thresholds. A config carries
  // either this or digital_leds, never both — the platform derives the v1
  // projection from digital_leds for older agents.
  digital_sources?: LEDSource[]
  // v2 form: per-LED source plus gradient bounds and brightness.
  digital_leds?: DigitalLEDSpec[]
}

export type LEDCapabilities = {
  stealth_available: boolean
  digital_leds: number
  // The ambient sources the node's AGENT advertises it can render; absent on
  // backends predating LED v2. The panel offers exactly these (SetLED refuses
  // a source the agent has not advertised).
  supported_sources?: LEDSource[]
}

export type LEDCommandRef = {
  id: string
  status: string
  details?: string
}

export type LEDState = {
  // Which level supplied `desired`: node | ou | root_ou | account.
  desired_source?: string
  desired?: LEDConfig
  desired_hash?: string
  applied_hash?: string
  in_sync: boolean
  queued: boolean
  last_command?: LEDCommandRef
  capabilities: LEDCapabilities
  // True when the device-profile resolver failed: capabilities is then empty
  // because nothing could be read, not because the board declares nothing.
  capabilities_unknown?: boolean
}

export const getLEDConfig = async (nodeId: string): Promise<LEDState> => {
  const response = await apiClient.get<ApiResponse<LEDState>>(
    `/api/v1/nodes/${nodeId}/led-config`,
  )
  return response.data.data
}

export const putLEDConfig = async (
  nodeId: string,
  cfg: LEDConfig,
): Promise<LEDState> => {
  const response = await apiClient.put<ApiResponse<LEDState>>(
    `/api/v1/nodes/${nodeId}/led-config`,
    cfg,
  )
  return response.data.data
}

export const resetLEDConfig = async (nodeId: string): Promise<LEDState> => {
  const response = await apiClient.delete<ApiResponse<LEDState>>(
    `/api/v1/nodes/${nodeId}/led-config`,
  )
  return response.data.data
}
