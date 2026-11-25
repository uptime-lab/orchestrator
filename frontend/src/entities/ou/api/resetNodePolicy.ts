import { apiClient } from '@/shared/utils/apiConfig'

// Clears every per-node policy override so the node inherits from its unit
// chain again. The backend pushes the access config when an SSH password login
// override was cleared.
export const resetNodePolicy = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/policy/reset`)
}
