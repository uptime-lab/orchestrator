import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// An open request to have the account erased.
//
// There is no `status` field, and that is not an omission: a request either
// exists or it does not. A "completed" state could never be reported, because by
// then there is no account left to ask.
export type AccountDeletionRequest = {
  id: string
  account_id: string
  requested_by: string
  requested_at: number
  reason?: string
  // Server-supplied, and rendered verbatim rather than restated in the UI: it
  // says the account is NOT deleted yet, which is the one thing a customer must
  // not conclude otherwise.
  notice: string
}

export const postAccountDeletionRequest = async (
  accountName: string,
  reason?: string,
): Promise<AccountDeletionRequest> => {
  const response = await apiClient.post<ApiResponse<AccountDeletionRequest>>(
    '/api/v1/account/deletion-request',
    { account_name: accountName, reason },
  )
  return response.data.data
}

// Returns null when no request is open — a 404 is the ordinary answer here, not
// an error, so it is translated rather than thrown.
export const getAccountDeletionRequest =
  async (): Promise<AccountDeletionRequest | null> => {
    try {
      const response = await apiClient.get<ApiResponse<AccountDeletionRequest>>(
        '/api/v1/account/deletion-request',
      )
      return response.data.data
    } catch (e: unknown) {
      const status = (e as { response?: { status?: number } })?.response?.status
      if (status === 404) return null
      throw e
    }
  }

export const deleteAccountDeletionRequest = async (): Promise<void> => {
  await apiClient.delete('/api/v1/account/deletion-request')
}
