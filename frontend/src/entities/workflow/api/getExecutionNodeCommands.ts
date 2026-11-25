import type { ApiResponse } from '@/entities/auth/api/postSignIn'
import type { NodeCommand } from '@/entities/node/api/types'

import { apiClient } from '@/shared/utils/apiConfig'

export const getExecutionNodeCommands = async (
  executionId: string,
  nodeId: string,
): Promise<NodeCommand[]> => {
  const response = await apiClient.get<ApiResponse<NodeCommand[]>>(
    `/api/v1/workflow-executions/${executionId}/nodes/${nodeId}/commands`,
  )
  return response.data.data
}
