import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { ImageDownloadResult } from './types.ts'

// Mints a short-lived, self-authenticating URL for the image bytes.
//
// The download is deliberately a two-step (ask, then navigate) rather than a
// direct link: the API needs an Authorization header and a browser navigation
// cannot carry one, while fetching multi-gigabyte bytes through axios would
// buffer the whole image in the tab. The returned URL carries its own
// capability, so `window.location.assign` on it just works.
export const getImageDownload = async (
  id: string,
): Promise<ImageDownloadResult> => {
  const response = await apiClient.get<ApiResponse<ImageDownloadResult>>(
    `/api/v1/images/${id}/download`,
  )
  return response.data.data
}
