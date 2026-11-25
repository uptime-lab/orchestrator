import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type {
  WorkflowExecutionListParams,
  WorkflowExecutionListResult,
} from './types.ts'

export const getWorkflowExecutions = async (
  params: WorkflowExecutionListParams = {},
): Promise<WorkflowExecutionListResult> => {
  const query: Record<string, string | number> = {}
  if (params.templateId) query.template_id = params.templateId
  if (params.ouId) query.ou_id = params.ouId
  if (params.page) query.page = params.page
  if (params.perPage) query.per_page = params.perPage
  const response = await apiClient.get<
    ApiResponse<WorkflowExecutionListResult>
  >('/api/v1/workflow-executions', { params: query })
  return response.data.data
}
