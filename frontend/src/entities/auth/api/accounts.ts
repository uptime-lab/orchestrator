import { apiClient } from '@/shared/utils/apiConfig'

import { AccountOption, ApiResponse } from './postSignIn'

export type SwitchAccountResult = {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
}

// getAccounts returns the accounts the current user can switch into.
export const getAccounts = async (): Promise<AccountOption[]> => {
  const response =
    await apiClient.get<ApiResponse<AccountOption[]>>('/auth/accounts')
  return response.data.data
}

// postSwitchAccount mints a token pair pinned to another account.
export const postSwitchAccount = async (
  accountId: string,
): Promise<SwitchAccountResult> => {
  const response = await apiClient.post<ApiResponse<SwitchAccountResult>>(
    '/auth/switch-account',
    { account_id: accountId },
  )
  return response.data.data
}
