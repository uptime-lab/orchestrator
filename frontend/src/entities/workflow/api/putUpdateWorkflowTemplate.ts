import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowGraph, WorkflowStep, WorkflowTemplate } from './types.ts'

export type UpdateWorkflowTemplatePayload = {
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
  // The updated_at this client last read. When sent, the server refuses the
  // save with 409 if the stored playbook has moved on — this is a
  // full-document PUT, so without it one operator's save reverts another's.
  expected_updated_at?: string
}

export type UpdateWorkflowTemplateResponse = WorkflowTemplate

export const putUpdateWorkflowTemplate = async (
  templateId: string,
  payload: UpdateWorkflowTemplatePayload,
): Promise<UpdateWorkflowTemplateResponse> => {
  const response = await apiClient.put<ApiResponse<WorkflowTemplate>>(
    `/api/v1/workflow-templates/${templateId}`,
    payload,
  )
  return response.data.data
}
