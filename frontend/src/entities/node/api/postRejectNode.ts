import { apiClient } from '@/shared/utils/apiConfig'

export const rejectNode = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/reject`)
}
