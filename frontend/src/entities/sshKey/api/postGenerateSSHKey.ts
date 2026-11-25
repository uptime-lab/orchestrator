import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SSHKeyResult } from './types.ts'

export type GenerateSSHKeyRequest = {
  alias: string
  tags?: string[]
}

// generateSSHKey asks the backend to generate an ed25519 connection keypair. The
// private key stays encrypted server-side; only the public part is downloadable.
export const generateSSHKey = async (
  data: GenerateSSHKeyRequest,
): Promise<SSHKeyResult> => {
  const response = await apiClient.post<ApiResponse<SSHKeyResult>>(
    '/api/v1/ssh-keys/generate',
    data,
  )
  return response.data.data
}
