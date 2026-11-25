import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeResult } from './types.ts'

export type UpdateNodePayload = {
  name?: string
  // hostname and ipv4 are intentionally excluded - they can only be set during node registration
  ssh_key_id?: string | null // Optional: SSH key ID for provisioning, null to clear
  default_username?: string // Optional: Default username for provisioning
  default_password?: string // Optional: Default password for provisioning
  notes?: string // Optional: free-form operator notes
}

export type UpdateNodeResponse = NodeResult

export const putUpdateNode = async (
  nodeId: string,
  payload: UpdateNodePayload,
): Promise<UpdateNodeResponse> => {
  const response = await apiClient.put<ApiResponse<NodeResult>>(
    `/api/v1/nodes/${nodeId}`,
    payload,
  )
  return response.data.data
}
