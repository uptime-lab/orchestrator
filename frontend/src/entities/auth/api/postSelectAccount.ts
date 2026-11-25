import { publicApiClient } from '@/shared/utils/apiConfig'

import { ApiResponse, AuthResult } from './postSignIn'

export type SelectAccountPayload = {
  pre_token: string
  account_id: string
}

// postSelectAccount exchanges the pre-auth token for an account-pinned pair.
// It uses publicApiClient: there is no stored access token yet.
export const postSelectAccount = async (
  payload: SelectAccountPayload,
): Promise<AuthResult> => {
  const response = await publicApiClient.post<ApiResponse<AuthResult>>(
    '/auth/select-account',
    payload,
  )
  return response.data.data
}
