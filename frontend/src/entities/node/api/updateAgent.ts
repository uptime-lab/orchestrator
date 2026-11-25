import { apiClient } from '@/shared/utils/apiConfig'

// Tells a running managed node to self-update to the given agent version
// (downloads from the platform CDN, installs over the live OS, restarts).
// The agent refuses versions older than the one it runs unless allowDowngrade
// is set (anti-rollback).
export const updateAgent = async (
  nodeId: string,
  agentVersion: string,
  allowDowngrade = false,
): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/command`, {
    command: 'update_agent',
    params: { agent_version: agentVersion, allow_downgrade: allowDowngrade },
  })
}
