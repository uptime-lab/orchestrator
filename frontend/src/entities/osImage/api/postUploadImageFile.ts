import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { ImageResult } from './types.ts'

export type UploadProgress = {
  loadedBytes: number
  totalBytes: number
  percent: number
}

export type UploadImageFileOptions = {
  onProgress?: (progress: UploadProgress) => void
  signal?: AbortSignal
}

export const postUploadImageFile = async (
  id: string,
  file: File,
  options?: UploadImageFileOptions,
): Promise<ImageResult> => {
  const formData = new FormData()
  formData.append('file', file)

  const response = await apiClient.post<ApiResponse<ImageResult>>(
    `/api/v1/images/${id}/upload`,
    formData,
    {
      headers: {
        'Content-Type': 'multipart/form-data',
      },
      // OS images are multi-GB; the client-wide timeout would abort them.
      timeout: 0,
      signal: options?.signal,
      onUploadProgress: (event) => {
        const totalBytes = event.total ?? file.size
        if (!options?.onProgress || !totalBytes) {
          return
        }
        options.onProgress({
          loadedBytes: event.loaded,
          totalBytes,
          percent: Math.min(100, Math.round((event.loaded / totalBytes) * 100)),
        })
      },
    },
  )
  return response.data.data
}
