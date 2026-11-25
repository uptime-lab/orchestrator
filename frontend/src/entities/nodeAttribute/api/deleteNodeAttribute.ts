import { apiClient } from '@/shared/utils/apiConfig'

export const deleteNodeAttribute = async (
  nodeId: string,
  key: string,
): Promise<void> => {
  await apiClient.delete(
    `/api/v1/nodes/${nodeId}/attributes/${encodeURIComponent(key)}`,
  )
}
