import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type {
  ReplaceSecretAttachmentsRequest,
  SecretAttachmentListResult,
} from './types'

export const replaceNodeSecretAttachments = async (
  nodeId: string,
  data: ReplaceSecretAttachmentsRequest,
): Promise<SecretAttachmentListResult> => {
  const response = await apiClient.put<ApiResponse<SecretAttachmentListResult>>(
    `/api/v1/nodes/${nodeId}/secret-attachments`,
    data,
  )
  return response.data.data
}

export const replaceOUSecretAttachments = async (
  ouId: string,
  data: ReplaceSecretAttachmentsRequest,
): Promise<SecretAttachmentListResult> => {
  const response = await apiClient.put<ApiResponse<SecretAttachmentListResult>>(
    `/api/v1/organizational-units/${ouId}/secret-attachments`,
    data,
  )
  return response.data.data
}
