import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { Account } from './types'

// getAccount reads the caller's own account (the route refuses any other id).
// Unlike the /auth/accounts listing this carries the full settings —
// identify_duration_sec in particular — so settings pages read from here.
export const getAccount = async (accountId: string): Promise<Account> => {
  const response = await apiClient.get<ApiResponse<Account>>(
    `/api/v1/accounts/${accountId}`,
  )
  return response.data.data
}
