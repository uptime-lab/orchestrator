import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowExecution } from './types.ts'

// Cancel settles a run that is stuck — a deleted node, a dropped enqueue, a
// block whose agent never answered — and frees the per-account active slot it
// consumes. The endpoint existed before this caller; nothing called
// it, so a wedged run had no remedy anywhere in the product.
export const postCancelWorkflowExecution = async (
  executionId: string,
): Promise<WorkflowExecution> => {
  const response = await apiClient.post<ApiResponse<WorkflowExecution>>(
    `/api/v1/workflow-executions/${executionId}/cancel`,
  )
  return response.data.data
}
