import { apiClient } from '@/shared/utils/apiConfig'

export const rebootNode = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/command`, {
    command: 'reboot',
    params: {
      delay_seconds: 0,
    },
  })
}
