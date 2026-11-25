import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { CreateSecretRequest, Secret } from './types'

export const postCreateSecret = async (
  data: CreateSecretRequest,
): Promise<Secret> => {
  const response = await apiClient.post<ApiResponse<Secret>>(
    '/api/v1/secrets',
    data,
  )
  return response.data.data
}
