import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowTemplate } from './types.ts'

export const getWorkflowTemplates = async (filters?: {
  name?: string
  is_shortcut?: boolean
}): Promise<WorkflowTemplate[]> => {
  const params: Record<string, string> = {}
  if (filters?.name) {
    params.name = filters.name
  }
  if (filters?.is_shortcut !== undefined) {
    params.is_shortcut = filters.is_shortcut ? 'true' : 'false'
  }
  const response = await apiClient.get<ApiResponse<WorkflowTemplate[]>>(
    '/api/v1/workflow-templates',
    { params },
  )
  return response.data.data
}
