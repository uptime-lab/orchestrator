import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { AttachSecretRequest, SecretAttachment } from './types'

// Attach a tag to an OU or a node: every secret carrying the tag is delivered
// there (dynamic membership).
export const attachSecretTag = async (
  tag: string,
  data: AttachSecretRequest,
): Promise<SecretAttachment> => {
  const response = await apiClient.post<ApiResponse<SecretAttachment>>(
    `/api/v1/secret-tags/${encodeURIComponent(tag)}/attachments`,
    data,
  )
  return response.data.data
}
