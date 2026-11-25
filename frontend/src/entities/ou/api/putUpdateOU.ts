import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { OU, UpdateOURequest } from './types'

export const putUpdateOU = async (
  ouId: string,
  data: UpdateOURequest,
): Promise<OU> => {
  const response = await apiClient.put<ApiResponse<OU>>(
    `/api/v1/organizational-units/${ouId}`,
    data,
  )
  return response.data.data
}
