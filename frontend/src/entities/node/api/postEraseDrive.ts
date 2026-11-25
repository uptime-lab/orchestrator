import { apiClient } from '@/shared/utils/apiConfig'

export type EraseDriveRequest = {
  target_device: string
}

export const postEraseDrive = async (
  nodeId: string,
  devicePath: string,
): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/command`, {
    command: 'erase_drive',
    params: {
      target_device: devicePath,
    },
  })
}
