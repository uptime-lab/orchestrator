import { apiClient } from '@/shared/utils/apiConfig'

export type ProvisionConnectionRequest = {
  target_device: string
  root_partition?: number
}

export const postProvisionConnection = async (
  nodeId: string,
  request: ProvisionConnectionRequest,
): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/command`, {
    command: 'provision_connection',
    params: {
      target_device: request.target_device,
      root_partition: request.root_partition,
    },
  })
}
