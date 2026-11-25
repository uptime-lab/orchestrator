import { apiClient } from '@/shared/utils/apiConfig'

export type InstallAgentRequest = {
  target_device: string
  root_partition?: number
  agent_version: string
  // full (default) = the managing agent; watcher = the telemetry-only
  // Watcher. Refused by the platform when the recovery agent predates the
  // variant field (it would silently install the full agent instead).
  variant?: 'full' | 'watcher'
}

export const postInstallAgent = async (
  nodeId: string,
  request: InstallAgentRequest,
): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/command`, {
    command: 'install_agent',
    params: {
      target_device: request.target_device,
      root_partition: request.root_partition,
      agent_version: request.agent_version,
      variant: request.variant,
    },
  })
}
