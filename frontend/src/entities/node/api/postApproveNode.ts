import { apiClient } from '@/shared/utils/apiConfig'

export const approveNode = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/approve`)
}
