import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeCommand, NodeCommandListResult } from './types.ts'

export const getNodeCommands = async (
  nodeId: string,
  limit = 50,
  offset = 0,
): Promise<NodeCommand[]> => {
  const response = await apiClient.get<ApiResponse<NodeCommandListResult>>(
    `/api/v1/nodes/${nodeId}/node-commands?limit=${limit}&offset=${offset}`,
  )
  return response.data.data.commands
}

export const getNodeCommandById = async (
  commandId: string,
): Promise<NodeCommand> => {
  const response = await apiClient.get<ApiResponse<NodeCommand>>(
    `/api/v1/node-commands/${commandId}`,
  )
  return response.data.data
}
