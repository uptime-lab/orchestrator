import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SecretListResult } from './types'

export const getNodeSecrets = async (
  nodeId: string,
): Promise<SecretListResult> => {
  const response = await apiClient.get<ApiResponse<SecretListResult>>(
    `/api/v1/nodes/${nodeId}/secrets`,
  )
  return response.data.data
}
