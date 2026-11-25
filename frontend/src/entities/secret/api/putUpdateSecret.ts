import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { Secret, UpdateSecretRequest } from './types'

export const putUpdateSecret = async (
  secretId: string,
  data: UpdateSecretRequest,
): Promise<Secret> => {
  const response = await apiClient.put<ApiResponse<Secret>>(
    `/api/v1/secrets/${secretId}`,
    data,
  )
  return response.data.data
}
