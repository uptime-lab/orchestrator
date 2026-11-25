import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { ImageListResult, ImageResult } from './types.ts'

export const getImages = async (): Promise<ImageResult[]> => {
  const response =
    await apiClient.get<ApiResponse<ImageListResult>>('/api/v1/images')
  return response.data.data.images
}
