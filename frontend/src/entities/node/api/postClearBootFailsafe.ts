import { apiClient } from '@/shared/utils/apiConfig'

// Acknowledge and clear a node's boot-loop failsafe flag (the "auto-recovered"
// badge): resets the withheld-attempt counter and clears boot_failsafe_at.
export const postClearBootFailsafe = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/clear-boot-failsafe`)
}
