import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowExecution } from './types.ts'

export type StartWorkflowExecutionPayload = {
  template_id: string
  node_ids: string[]
}

export type StartWorkflowExecutionResponse = WorkflowExecution

export const postStartWorkflowExecution = async (
  payload: StartWorkflowExecutionPayload,
): Promise<StartWorkflowExecutionResponse> => {
  const response = await apiClient.post<ApiResponse<WorkflowExecution>>(
    '/api/v1/workflow-executions',
    payload,
  )
  return response.data.data
}
