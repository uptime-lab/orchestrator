import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { OUListResult } from './types'

export const getOUs = async (): Promise<OUListResult> => {
  const response = await apiClient.get<ApiResponse<OUListResult>>(
    '/api/v1/organizational-units',
  )
  return response.data.data
}
