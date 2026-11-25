import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// An OS account really present on the device (distinct from Orchestrator-managed
// access entries). Read-only inventory.
export type OSUser = {
  username: string
  uid: number
  gid: number
  home: string
  shell: string
  managed: boolean
}

export type OSUserScanResult = {
  status: 'running' | 'completed' | 'failed'
  detail?: string
  users?: OSUser[]
}

// Dispatch a scan; returns the command id to poll. Online-only.
export const scanOSUsers = async (nodeId: string): Promise<string> => {
  const response = await apiClient.post<ApiResponse<{ command_id: string }>>(
    `/api/v1/nodes/${nodeId}/os-users/scan`,
  )
  return response.data.data.command_id
}

// Poll a dispatched scan; the server parses raw output into the inventory.
export const getOSUserScan = async (
  nodeId: string,
  commandId: string,
): Promise<OSUserScanResult> => {
  const response = await apiClient.get<ApiResponse<OSUserScanResult>>(
    `/api/v1/nodes/${nodeId}/os-users/scan/${commandId}`,
  )
  return response.data.data
}

// One KEY=VALUE line of the live rpi-eeprom-config.
export type EEPROMConfigEntry = { key: string; value: string }

export type EEPROMConfigScanResult = {
  status: 'running' | 'completed' | 'failed'
  detail?: string
  entries?: EEPROMConfigEntry[]
}

// Dispatch a read of the actually-flashed EEPROM config; returns the command id.
export const scanEEPROMConfig = async (nodeId: string): Promise<string> => {
  const response = await apiClient.post<ApiResponse<{ command_id: string }>>(
    `/api/v1/nodes/${nodeId}/eeprom-config/scan`,
  )
  return response.data.data.command_id
}

export const getEEPROMConfigScan = async (
  nodeId: string,
  commandId: string,
): Promise<EEPROMConfigScanResult> => {
  const response = await apiClient.get<ApiResponse<EEPROMConfigScanResult>>(
    `/api/v1/nodes/${nodeId}/eeprom-config/scan/${commandId}`,
  )
  return response.data.data
}
