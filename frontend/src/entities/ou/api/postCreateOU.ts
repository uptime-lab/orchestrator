import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { CreateOURequest, OU } from './types'

export const postCreateOU = async (data: CreateOURequest): Promise<OU> => {
  const response = await apiClient.post<ApiResponse<OU>>(
    '/api/v1/organizational-units',
    data,
  )
  return response.data.data
}
