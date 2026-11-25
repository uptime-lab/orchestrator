import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeFile, NodeFileListResult } from './types.ts'

export const getFiles = async (nodeId: string): Promise<NodeFileListResult> => {
  const response = await apiClient.get<ApiResponse<{ files: NodeFile[] }>>(
    `/api/v1/nodes/${nodeId}/files`,
  )
  return { files: response.data.data.files || [] }
}

// Legacy alias for backward compatibility
export const getConfigFiles = getFiles
