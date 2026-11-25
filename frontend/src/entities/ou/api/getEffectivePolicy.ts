import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { EffectivePolicyResult } from './types'

export const getEffectivePolicy = async (
  nodeId: string,
): Promise<EffectivePolicyResult> => {
  const response = await apiClient.get<ApiResponse<EffectivePolicyResult>>(
    `/api/v1/nodes/${nodeId}/effective-policy`,
  )
  return response.data.data
}
