import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SSHKeyResult } from './types.ts'

export type UpdateSSHKeyRequest = {
  // Omitted = unchanged, [] = clear, list = replace.
  tags?: string[]
  alias?: string
  key_content?: string
  is_default?: boolean
}

export const putUpdateSSHKey = async (
  id: string,
  data: UpdateSSHKeyRequest,
): Promise<SSHKeyResult> => {
  const response = await apiClient.put<ApiResponse<SSHKeyResult>>(
    `/api/v1/ssh-keys/${id}`,
    data,
  )
  return response.data.data
}
