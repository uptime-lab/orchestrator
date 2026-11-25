import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { ImageResult } from './types.ts'

export const getImage = async (id: string): Promise<ImageResult> => {
  const response = await apiClient.get<ApiResponse<ImageResult>>(
    `/api/v1/images/${id}`,
  )
  return response.data.data
}
