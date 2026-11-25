import { apiClient } from '@/shared/utils/apiConfig'

/**
 * Delete a node.
 *
 * `force` is required when the device is ONLINE, and the platform answers 409
 * without it. That is not ceremony: deleting an online node is the one moment
 * the platform can still reach the device, and deleting there leaves the agent
 * holding an identity nothing will accept again — it retries forever, powered
 * and invisible. A forced delete also asks the agent to release its own adoption
 * file so the device returns to a clean unadopted state (agent >= 0.5.0; older
 * ones are refused by the version floor, and the deleted-node record is the
 * fallback).
 */
export const deleteNode = async (
  nodeId: string,
  force = false,
): Promise<void> => {
  await apiClient.delete(`/api/v1/nodes/${nodeId}`, {
    params: force ? { force: true } : undefined,
  })
}
