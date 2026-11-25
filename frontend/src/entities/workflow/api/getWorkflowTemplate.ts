import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowTemplate } from './types.ts'

export const getWorkflowTemplate = async (
  templateId: string,
): Promise<WorkflowTemplate> => {
  const response = await apiClient.get<ApiResponse<WorkflowTemplate>>(
    `/api/v1/workflow-templates/${templateId}`,
  )
  return response.data.data
}
