import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SecretAttachmentListResult } from './types'

// A tag's attachments: which units and nodes it delivers to.
//
// Membership is dynamic — a secret tagged tomorrow is delivered through these
// same attachments — so this answers "where does this tag reach", not "which
// secrets are on it".
export const getSecretTagAttachments = async (
  tag: string,
): Promise<SecretAttachmentListResult> => {
  const response = await apiClient.get<ApiResponse<SecretAttachmentListResult>>(
    `/api/v1/secret-tags/${encodeURIComponent(tag)}/attachments`,
  )
  return response.data.data
}
