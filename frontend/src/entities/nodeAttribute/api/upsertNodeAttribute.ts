import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeAttribute, UpsertNodeAttributeRequest } from './types'

export const upsertNodeAttribute = async (
  nodeId: string,
  key: string,
  data: UpsertNodeAttributeRequest,
): Promise<NodeAttribute> => {
  const response = await apiClient.put<ApiResponse<NodeAttribute>>(
    `/api/v1/nodes/${nodeId}/attributes/${encodeURIComponent(key)}`,
    data,
  )
  return response.data.data
}
