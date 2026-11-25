import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowExecutionNode } from './types.ts'

export const getWorkflowExecutionNodes = async (
  executionId: string,
): Promise<WorkflowExecutionNode[]> => {
  const response = await apiClient.get<ApiResponse<WorkflowExecutionNode[]>>(
    `/api/v1/workflow-executions/${executionId}/nodes`,
  )
  return response.data.data
}
