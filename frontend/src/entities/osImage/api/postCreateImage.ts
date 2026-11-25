import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { ImageCreateCommand, ImageResult } from './types.ts'

export const postCreateImage = async (
  payload: ImageCreateCommand,
): Promise<ImageResult> => {
  const response = await apiClient.post<ApiResponse<ImageResult>>(
    '/api/v1/images',
    payload,
  )
  return response.data.data
}
