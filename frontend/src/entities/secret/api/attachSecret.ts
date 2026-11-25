import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { AttachSecretRequest, SecretAttachment } from './types'

export const attachSecret = async (
  secretId: string,
  data: AttachSecretRequest,
): Promise<SecretAttachment> => {
  const response = await apiClient.post<ApiResponse<SecretAttachment>>(
    `/api/v1/secrets/${secretId}/attachments`,
    data,
  )
  return response.data.data
}
