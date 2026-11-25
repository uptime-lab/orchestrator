import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeFile } from './types.ts'

export const getFile = async (
  nodeId: string,
  path: string,
  version?: number,
): Promise<NodeFile> => {
  // The path travels as a query parameter: the old `/files/<encoded path>`
  // route was shadowed by `/files/*/versions`, so it never reached its handler.
  const response = await apiClient.get<ApiResponse<NodeFile>>(
    `/api/v1/nodes/${nodeId}/files/content`,
    { params: version ? { path, version } : { path } },
  )
  return response.data.data as NodeFile
}

// Legacy alias for backward compatibility
export const getConfigFile = getFile
