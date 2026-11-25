import { apiClient } from '@/shared/utils/apiConfig'

export const deleteWorkflowTemplate = async (
  templateId: string,
): Promise<void> => {
  await apiClient.delete(`/api/v1/workflow-templates/${templateId}`)
}
