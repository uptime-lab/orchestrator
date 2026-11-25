import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SSHKeyResult, SSHKeyType } from './types.ts'

export type CreateSSHKeyRequest = {
  tags?: string[]
  alias: string
  key_type?: SSHKeyType
  key_content: string
}

export const postCreateSSHKey = async (
  data: CreateSSHKeyRequest,
): Promise<SSHKeyResult> => {
  const response = await apiClient.post<ApiResponse<SSHKeyResult>>(
    '/api/v1/ssh-keys',
    data,
  )
  return response.data.data
}
