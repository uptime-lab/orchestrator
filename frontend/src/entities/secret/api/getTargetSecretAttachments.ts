import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SecretAttachmentListResult } from './types'

// A target's own attachments (secrets and tags, with attachment ids). Unlike
// the effective set, it lists a tag no secret carries yet.
export const getNodeSecretAttachments = async (
  nodeId: string,
): Promise<SecretAttachmentListResult> => {
  const response = await apiClient.get<ApiResponse<SecretAttachmentListResult>>(
    `/api/v1/nodes/${nodeId}/secret-attachments`,
  )
  return response.data.data
}

export const getOUSecretAttachments = async (
  ouId: string,
): Promise<SecretAttachmentListResult> => {
  const response = await apiClient.get<ApiResponse<SecretAttachmentListResult>>(
    `/api/v1/organizational-units/${ouId}/secret-attachments`,
  )
  return response.data.data
}
