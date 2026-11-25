import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { Secret } from './types'

export const getSecret = async (secretId: string): Promise<Secret> => {
  const response = await apiClient.get<ApiResponse<Secret>>(
    `/api/v1/secrets/${secretId}`,
  )
  return response.data.data
}
