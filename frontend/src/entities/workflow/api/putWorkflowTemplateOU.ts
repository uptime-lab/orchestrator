import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { WorkflowTemplate } from './types.ts'

/**
 * Re-target a playbook to an organizational unit (null clears the scope).
 *
 * Its own endpoint on purpose. Doing this through the full-document PUT means
 * re-sending every field from whatever copy the client happens to hold, so a
 * drag onto a unit silently reverts anything another operator saved since this
 * page loaded — and on a cron playbook containing erase_drive, the field most
 * worth not reverting is this very one.
 */
// Sends the whole SET, so this one call both adds and removes a unit. An empty
// array clears the scope, which is why it is sent as ou_ids rather than a null
// ou_id: the server cannot otherwise tell "no units" from "not supplied".
export const putWorkflowTemplateOUs = async (
  templateId: string,
  ouIds: string[],
): Promise<WorkflowTemplate> => {
  const response = await apiClient.put<ApiResponse<WorkflowTemplate>>(
    `/api/v1/workflow-templates/${templateId}/organizational-unit`,
    { ou_ids: ouIds },
  )
  return response.data.data
}
