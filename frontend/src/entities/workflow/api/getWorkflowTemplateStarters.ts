import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowStarter } from './types.ts'

export const getWorkflowTemplateStarters = async (): Promise<
  WorkflowStarter[]
> => {
  const response = await apiClient.get<ApiResponse<WorkflowStarter[]>>(
    '/api/v1/workflow-templates/starters',
  )
  return response.data.data
}
