import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

export type NodeCommandRequest = {
  command: string // "reboot", "shutdown", "install_os", "install_agent", "provision_connection", "update_boot_order"
  params?: Record<string, unknown>
}

// CommandAccepted is the generic command route's answer. `queued` means a
// queueable command was registered for a node that is offline: nothing has
// reached the device yet, it is delivered when the node reconnects. Older
// servers send neither field.
export type CommandAccepted = {
  delivery?: 'dispatched' | 'queued'
  command_id?: string
}

export const postNodeCommand = async (
  nodeId: string,
  request: NodeCommandRequest,
): Promise<CommandAccepted> => {
  const response = await apiClient.post<ApiResponse<CommandAccepted>>(
    `/api/v1/nodes/${nodeId}/command`,
    request,
  )
  return response.data?.data ?? {}
}
