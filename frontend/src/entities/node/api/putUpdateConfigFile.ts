import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

export const updateFile = async (
  nodeId: string,
  path: string,
  content: string,
): Promise<{
  path: string
  version: number
  status: string
  message: string
}> => {
  const response = await apiClient.put<
    ApiResponse<{
      path: string
      version: number
      status: string
      message: string
    }>
  >(`/api/v1/nodes/${nodeId}/files/content`, { path, content })
  return response.data.data
}

// Legacy alias for backward compatibility
export const updateConfigFile = updateFile
