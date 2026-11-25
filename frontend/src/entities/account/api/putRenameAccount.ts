import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { Account } from './types'

// putRenameAccount renames the account. Server-side the action is
// super_admin-only and non-delegable, and it refuses to touch any account other
// than the caller's own.
export const putRenameAccount = async (
  accountId: string,
  name: string,
): Promise<Account> => {
  const response = await apiClient.put<ApiResponse<Account>>(
    `/api/v1/accounts/${accountId}`,
    { name },
  )
  return response.data.data
}

// putAccountTwoFactorPolicy toggles the "every password user must enroll a
// second factor" requirement (federated-only members are exempt). The name is deliberately NOT sent: it used to ride
// along (the update command once required it), which silently reverted a
// rename another operator saved between this page's mount and the toggle.
// Server-side an omitted name means "unchanged".
export const putAccountTwoFactorPolicy = async (
  accountId: string,
  required: boolean,
): Promise<Account> => {
  const response = await apiClient.put<ApiResponse<Account>>(
    `/api/v1/accounts/${accountId}`,
    { require_two_factor: required },
  )
  return response.data.data
}

// putAccountIdentifyDuration sets how long identify blinks run account-wide
// (device LEDs and the web lamp alike). Name-less for the same reason as the
// 2FA toggle above.
export const putAccountIdentifyDuration = async (
  accountId: string,
  seconds: number,
): Promise<Account> => {
  const response = await apiClient.put<ApiResponse<Account>>(
    `/api/v1/accounts/${accountId}`,
    { identify_duration_sec: seconds },
  )
  return response.data.data
}
