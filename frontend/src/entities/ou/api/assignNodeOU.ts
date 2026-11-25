import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { AssignNodeOURequest } from './types'

export const assignNodeOU = async (
  nodeId: string,
  data: AssignNodeOURequest,
): Promise<void> => {
  await apiClient.put<ApiResponse<unknown>>(
    `/api/v1/nodes/${nodeId}/organizational-unit`,
    data,
  )
}
