import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { PortablePlaybookDocument, WorkflowTemplate } from './types.ts'

export type ImportWorkflowTemplatePayload = {
  // Scope the created draft to an organizational unit; null = all nodes
  // Legacy single-unit field, still accepted by the server; ou_ids is the
  // scope and wins when both are present.
  ou_id?: string | null
  ou_ids?: string[]
  document: PortablePlaybookDocument
}

export type ImportWorkflowTemplateResult = {
  template: WorkflowTemplate
  // Human-readable notes about references the server could not resolve in
  // this account (cleared image ids, missing secrets)
  warnings: string[]
}

export const postImportWorkflowTemplate = async (
  payload: ImportWorkflowTemplatePayload,
): Promise<ImportWorkflowTemplateResult> => {
  const response = await apiClient.post<
    ApiResponse<ImportWorkflowTemplateResult>
  >('/api/v1/workflow-templates/import', payload)
  return response.data.data
}
