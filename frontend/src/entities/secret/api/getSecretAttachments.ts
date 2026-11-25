import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SecretAttachmentListResult } from './types'

export const getSecretAttachments = async (
  secretId: string,
): Promise<SecretAttachmentListResult> => {
  const response = await apiClient.get<ApiResponse<SecretAttachmentListResult>>(
    `/api/v1/secrets/${secretId}/attachments`,
  )
  return response.data.data
}
