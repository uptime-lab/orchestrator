import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SetNodePolicyRequest } from './types'

export const setNodePolicy = async (
  nodeId: string,
  data: SetNodePolicyRequest,
): Promise<void> => {
  await apiClient.put<ApiResponse<unknown>>(
    `/api/v1/nodes/${nodeId}/policy`,
    data,
  )
}
