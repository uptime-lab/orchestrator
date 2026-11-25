import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { MasterTokenListResult } from './types'

export const getMasterTokens = async (): Promise<MasterTokenListResult> => {
  const response = await apiClient.get<ApiResponse<MasterTokenListResult>>(
    '/api/v1/master-tokens',
  )
  return response.data.data
}
