import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { ImageResult, ImageUpdateCommand } from './types.ts'

export const putUpdateImage = async (
  id: string,
  payload: ImageUpdateCommand,
): Promise<ImageResult> => {
  const response = await apiClient.put<ApiResponse<ImageResult>>(
    `/api/v1/images/${id}`,
    payload,
  )
  return response.data.data
}
