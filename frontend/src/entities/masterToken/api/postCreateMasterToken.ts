import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { CreateMasterTokenRequest, MasterTokenResult } from './types'

export const postCreateMasterToken = async (
  data: CreateMasterTokenRequest,
): Promise<MasterTokenResult> => {
  const response = await apiClient.post<ApiResponse<MasterTokenResult>>(
    '/api/v1/master-tokens',
    data,
  )
  return response.data.data
}
