import { apiClient } from '@/shared/utils/apiConfig'

import { BootOrder } from './types'

export const updateBootOrder = async (
  nodeId: string,
  bootOrder: BootOrder,
): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/command`, {
    command: 'update_boot_order',
    params: {
      boot_order: bootOrder,
    },
  })
}
