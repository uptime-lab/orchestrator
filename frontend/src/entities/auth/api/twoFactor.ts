import { apiClient, publicApiClient } from '@/shared/utils/apiConfig'

import { ApiResponse, AuthResult, LoginChallenge } from './postSignIn'

export type TwoFactorStatus = {
  enabled: boolean
  recovery_codes_remaining: number
}

export type TwoFactorEnrollment = {
  secret: string
  otpauth_url: string
  qr_code: string
}

// getTwoFactorStatus reports whether the signed-in user has a second factor.
export const getTwoFactorStatus = async (): Promise<TwoFactorStatus> => {
  const res = await apiClient.get<ApiResponse<TwoFactorStatus>>(
    '/api/v1/users/me/2fa',
  )
  return res.data.data
}

// postEnrollTwoFactor starts enrollment. Nothing is enabled until confirm —
// abandoning this flow leaves the account exactly as it was.
export const postEnrollTwoFactor = async (): Promise<TwoFactorEnrollment> => {
  const res = await apiClient.post<ApiResponse<TwoFactorEnrollment>>(
    '/api/v1/users/me/2fa/enroll',
  )
  return res.data.data
}

// postConfirmTwoFactor activates the factor and returns the recovery codes.
// They are shown exactly once — the server keeps only their digests.
export const postConfirmTwoFactor = async (code: string): Promise<string[]> => {
  const res = await apiClient.post<ApiResponse<{ recovery_codes: string[] }>>(
    '/api/v1/users/me/2fa/confirm',
    { code },
  )
  return res.data.data.recovery_codes
}

// deleteTwoFactor turns the factor off. A current code is required so a stolen
// session cannot strip the protection it exists to survive.
export const deleteTwoFactor = async (code: string): Promise<void> => {
  await apiClient.delete('/api/v1/users/me/2fa', { data: { code } })
}

// postCompleteTwoFactor finishes a login that asked for a second factor.
// Public: at this point there is no session yet, only the ticket.
export const postCompleteTwoFactor = async (
  twoFactorToken: string,
  code: string,
): Promise<
  | { kind: 'tokens'; result: AuthResult }
  | { kind: 'challenge'; challenge: LoginChallenge }
> => {
  const response = await publicApiClient.post<
    ApiResponse<AuthResult | LoginChallenge>
  >('/auth/2fa', { two_factor_token: twoFactorToken, code })
  const data = response.data.data
  if ('pre_token' in data) {
    return { kind: 'challenge', challenge: data }
  }
  return { kind: 'tokens', result: data }
}
