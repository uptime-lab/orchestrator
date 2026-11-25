import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SecretListResult } from './types'

export const getOUSecrets = async (ouId: string): Promise<SecretListResult> => {
  const response = await apiClient.get<ApiResponse<SecretListResult>>(
    `/api/v1/organizational-units/${ouId}/secrets`,
  )
  return response.data.data
}
