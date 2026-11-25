import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowGraph, WorkflowStep, WorkflowTemplate } from './types.ts'

export type CreateWorkflowTemplatePayload = {
  name: string
  description: string
  is_shortcut?: boolean
  steps: WorkflowStep[]
  // When provided, the graph is the source of truth and steps are derived
  // from it server-side
  graph?: WorkflowGraph
  // Scope to an organizational unit; null clears the scope (all nodes)
  // Legacy single-unit field, still accepted by the server; ou_ids is the
  // scope and wins when both are present.
  ou_id?: string | null
  ou_ids?: string[]
  // active (default) or draft; drafts are hidden from run pickers
  status?: 'active' | 'draft'
}

export type CreateWorkflowTemplateResponse = WorkflowTemplate

export const postCreateWorkflowTemplate = async (
  payload: CreateWorkflowTemplatePayload,
): Promise<CreateWorkflowTemplateResponse> => {
  const response = await apiClient.post<ApiResponse<WorkflowTemplate>>(
    '/api/v1/workflow-templates',
    payload,
  )
  return response.data.data
}
