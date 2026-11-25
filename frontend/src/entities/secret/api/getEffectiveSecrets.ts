import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { EffectiveSecretListResult } from './types'

export const getNodeEffectiveSecrets = async (
  nodeId: string,
): Promise<EffectiveSecretListResult> => {
  const response = await apiClient.get<ApiResponse<EffectiveSecretListResult>>(
    `/api/v1/nodes/${nodeId}/effective-secrets`,
  )
  return response.data.data
}

export const getOUEffectiveSecrets = async (
  ouId: string,
): Promise<EffectiveSecretListResult> => {
  const response = await apiClient.get<ApiResponse<EffectiveSecretListResult>>(
    `/api/v1/organizational-units/${ouId}/effective-secrets`,
  )
  return response.data.data
}
