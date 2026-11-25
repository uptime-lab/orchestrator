import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SecretListParams, SecretListResult } from './types'

export const getSecrets = async (
  filters?: SecretListParams,
): Promise<SecretListResult> => {
  const params: Record<string, string> = {}
  if (filters?.search) params.search = filters.search
  if (filters?.kind) params.kind = filters.kind
  if (filters?.sensitive !== undefined)
    params.sensitive = filters.sensitive ? 'true' : 'false'
  if (filters?.tags && filters.tags.length > 0)
    params.tags = filters.tags.join(',')
  if (filters?.ou_id) params.ou_id = filters.ou_id
  if (filters?.node_id) params.node_id = filters.node_id
  if (filters?.unattached) params.unattached = 'true'
  if (filters?.sort) params.sort = filters.sort
  if (filters?.order) params.order = filters.order
  if (filters?.page) params.page = String(filters.page)
  if (filters?.per_page) params.per_page = String(filters.per_page)
  const response = await apiClient.get<ApiResponse<SecretListResult>>(
    '/api/v1/secrets',
    { params },
  )
  return response.data.data
}
