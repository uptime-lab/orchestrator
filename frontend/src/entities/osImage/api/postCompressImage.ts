import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { ImageResult } from './types.ts'

// Starts server-side compression of a raw .img/.iso file to .xz.
// Progress is delivered via image_updated websocket events.
export const postCompressImage = async (id: string): Promise<ImageResult> => {
  const response = await apiClient.post<ApiResponse<ImageResult>>(
    `/api/v1/images/${id}/compress`,
  )
  return response.data.data
}
