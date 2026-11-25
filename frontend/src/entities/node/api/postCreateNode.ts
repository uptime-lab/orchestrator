import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeResult } from './types.ts'

export type CreateNodePayload = {
  name: string
  ssh_key_id?: string // Optional: if not provided, default SSH key will be used
  default_username: string // Required: default username for provisioning
  default_password: string // Required: default password for provisioning
  // Opt into one-shot network-first EEPROM provisioning on first boot.
  provision_network_first?: boolean
}

export type CreateNodeResponse = NodeResult

export const postCreateNode = async (
  payload: CreateNodePayload,
): Promise<CreateNodeResponse> => {
  const response = await apiClient.post<ApiResponse<NodeResult>>(
    '/api/v1/nodes',
    payload,
  )
  return response.data.data
}
