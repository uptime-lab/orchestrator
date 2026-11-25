import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { NodeData, NodePageResult } from './types.ts'

// One page per request, concatenated client-side: the live fleet store
// ($nodes) needs every row — WebSocket node_updated events merge into the full
// set — but a single unbounded response does not scale with the node cap.
// Server-side sorting matches the store's own (display name), so pages
// concatenate in the order the list renders.
const PER_PAGE = 200

export const getNodes = async (): Promise<NodeData[]> => {
  const all: NodeData[] = []
  for (let page = 1; ; page++) {
    const response = await apiClient.get<ApiResponse<NodePageResult>>(
      '/api/v1/nodes',
      { params: { page, per_page: PER_PAGE } },
    )
    const { nodes, total } = response.data.data
    all.push(...nodes)
    if (all.length >= total || nodes.length === 0) {
      return all
    }
  }
}
