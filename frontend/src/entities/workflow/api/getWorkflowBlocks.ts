import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowBlock } from './types.ts'

export const getWorkflowBlocks = async (): Promise<WorkflowBlock[]> => {
  const response = await apiClient.get<ApiResponse<WorkflowBlock[]>>(
    '/api/v1/workflow-blocks',
  )
  return response.data.data
}
