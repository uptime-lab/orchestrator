import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowExecution } from './types.ts'

export const getWorkflowExecution = async (
  executionId: string,
): Promise<WorkflowExecution> => {
  const response = await apiClient.get<ApiResponse<WorkflowExecution>>(
    `/api/v1/workflow-executions/${executionId}`,
  )
  return response.data.data
}
