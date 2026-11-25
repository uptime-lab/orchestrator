import { apiClient, publicApiClient } from '@/shared/utils/apiConfig'

import { ApiResponse } from './postSignIn'

// postVerifyEmail confirms an address with the one-time token from the email
// link. Public: the token in the body IS the credential, so this runs without
// a session — the link may well be opened in a browser that is not signed in.
export const postVerifyEmail = async (token: string): Promise<void> => {
  await publicApiClient.post<ApiResponse<{ verified: boolean }>>(
    '/auth/verify-email',
    { token },
  )
}

// postResendVerification mails a fresh link to the signed-in user, revoking the
// previous one. Throttled server-side (429 when requested too often).
export const postResendVerification = async (): Promise<void> => {
  await apiClient.post<ApiResponse<{ sent: boolean }>>(
    '/api/v1/users/me/resend-verification',
  )
}
