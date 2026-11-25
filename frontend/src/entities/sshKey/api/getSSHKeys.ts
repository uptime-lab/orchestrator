import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SSHKeyListResult, SSHKeyResult } from './types.ts'

export const getSSHKeys = async (): Promise<SSHKeyResult[]> => {
  try {
    const response =
      await apiClient.get<ApiResponse<SSHKeyListResult>>('/api/v1/ssh-keys')
    return response.data.data.keys
  } catch (error) {
    // Fail safely - return empty array if endpoint doesn't exist or errors
    // The diagnostic stays: this path SWALLOWS the failure, so without it a
    // broken endpoint is indistinguishable from "no key".
    // eslint-disable-next-line no-console
    console.warn('Failed to fetch SSH keys:', error)
    return []
  }
}
