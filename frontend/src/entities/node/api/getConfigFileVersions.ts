import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeFile, NodeFileVersionsResult } from './types.ts'

export const getFileVersions = async (
  nodeId: string,
  path: string,
): Promise<NodeFileVersionsResult> => {
  const response = await apiClient.get<ApiResponse<{ versions: NodeFile[] }>>(
    `/api/v1/nodes/${nodeId}/files/versions`,
    { params: { path } },
  )
  return { versions: response.data.data.versions || [] }
}

// Legacy alias for backward compatibility
export const getConfigFileVersions = getFileVersions
