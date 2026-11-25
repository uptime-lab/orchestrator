import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeFile } from './types.ts'

export const applyFile = async (
  nodeId: string,
  fileId: string,
): Promise<NodeFile> => {
  const response = await apiClient.post<ApiResponse<NodeFile>>(
    `/api/v1/nodes/${nodeId}/files/${fileId}/apply`,
  )
  return response.data.data
}

// Legacy alias for backward compatibility
export const applyConfigFile = applyFile
