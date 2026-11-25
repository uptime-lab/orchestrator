import { apiClient } from '@/shared/utils/apiConfig'

export const detachSecret = async (
  secretId: string,
  attachmentId: string,
): Promise<void> => {
  await apiClient.delete(
    `/api/v1/secrets/${secretId}/attachments/${attachmentId}`,
  )
}
