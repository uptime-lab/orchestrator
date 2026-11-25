import { apiClient } from '@/shared/utils/apiConfig'

export const shutdownNode = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/command`, {
    command: 'shutdown',
    params: {
      delay_seconds: 0,
    },
  })
}
