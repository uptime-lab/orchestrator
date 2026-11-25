import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// Overclock presets: the backend keeps a per-board catalog (Pi 4 / CM4 / Pi 5 /
// CM5) and applies a preset over the existing patch_file marker-block command
// into /boot/firmware/config.txt. The platform NEVER reboots the node itself —
// config.txt is read by the firmware at boot, so the operator reboots
// explicitly (reboot_required is always true on apply).

export type OverclockPreset = {
  name: string // "stock" | "balanced" | "performance"
  label: string
  description: string
  directives: string[] // config.txt lines; empty for stock
}

export type OverclockPresetsResponse = {
  // Detected board ("pi4"/"cm4"/"pi5"/"cm5"), or "" with empty presets when
  // the node's model has no catalog — the UI hides the picker then.
  board: string
  presets: OverclockPreset[]
}

export type OverclockApplyResponse = {
  board: string
  preset: string
  reboot_required: boolean
}

export const getOverclockPresets = async (
  nodeId: string,
): Promise<OverclockPresetsResponse> => {
  const response = await apiClient.get<ApiResponse<OverclockPresetsResponse>>(
    `/api/v1/nodes/${nodeId}/overclock-presets`,
  )
  return response.data.data
}

export const applyOverclockPreset = async (
  nodeId: string,
  preset: string,
): Promise<OverclockApplyResponse> => {
  const response = await apiClient.post<ApiResponse<OverclockApplyResponse>>(
    `/api/v1/nodes/${nodeId}/overclock`,
    { preset },
  )
  return response.data.data
}
