import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SSHKeyResult } from './types.ts'

export const getSSHKey = async (id: string): Promise<SSHKeyResult | null> => {
  try {
    const response = await apiClient.get<ApiResponse<SSHKeyResult>>(
      `/api/v1/ssh-keys/${id}`,
    )
    return response.data.data
  } catch (error) {
    // Fail safely - return null if endpoint doesn't exist or errors.
    // The diagnostic stays: this path SWALLOWS the failure, so without it a
    // broken endpoint is indistinguishable from "no key".
    // eslint-disable-next-line no-console
    console.warn('Failed to fetch SSH key:', error)
    return null
  }
}
