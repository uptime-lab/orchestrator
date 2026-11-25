import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

type FavoriteListResult = {
  node_ids: string[]
}

// Ids of the nodes the current user marked as favorite.
export const getFavoriteNodeIds = async (): Promise<string[]> => {
  const response = await apiClient.get<ApiResponse<FavoriteListResult>>(
    '/api/v1/nodes/favorites',
  )
  return response.data.data.node_ids
}

// Mark or unmark a node as favorite; returns the updated id list.
export const putNodeFavorite = async (
  nodeId: string,
  favorite: boolean,
): Promise<string[]> => {
  const response = await apiClient.put<ApiResponse<FavoriteListResult>>(
    `/api/v1/nodes/${nodeId}/favorite`,
    { favorite },
  )
  return response.data.data.node_ids
}
