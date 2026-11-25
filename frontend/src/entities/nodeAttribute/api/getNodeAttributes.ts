import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeAttributeListResult } from './types'

export const getNodeAttributes = async (
  nodeId: string,
): Promise<NodeAttributeListResult> => {
  const response = await apiClient.get<ApiResponse<NodeAttributeListResult>>(
    `/api/v1/nodes/${nodeId}/attributes`,
  )
  return response.data.data
}
