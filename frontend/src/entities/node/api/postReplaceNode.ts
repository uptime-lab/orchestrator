import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

export type ReplaceNodeRequest = {
  target_node_id: string
  template_id?: string
}

export type ReplaceNodeResult = {
  attributes_transferred: number
  hostname: string
  ou_transferred: boolean
  execution_id?: string
}

// Transfer a failed node's identity (custom attributes + hostname + OU) onto a
// spare node, optionally running a playbook on the target.
export const replaceNode = async (
  sourceNodeId: string,
  data: ReplaceNodeRequest,
): Promise<ReplaceNodeResult> => {
  const response = await apiClient.post<ApiResponse<ReplaceNodeResult>>(
    `/api/v1/nodes/${sourceNodeId}/replace`,
    data,
    // A 403 here is the playbook's unit scope refusing the target, which the
    // control shows in place; it is not a missing permission.
    { permissionToast: 'never' },
  )
  return response.data.data
}
