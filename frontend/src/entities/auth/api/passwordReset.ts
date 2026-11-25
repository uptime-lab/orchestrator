import { publicApiClient } from '@/shared/utils/apiConfig'

import { ApiResponse } from './postSignIn'

// postForgotPassword asks for a reset link. Public, and deliberately uniform:
// the server answers the same way for an unknown address as for a real one, so
// callers must not treat a 200 as proof the account exists.
export const postForgotPassword = async (email: string): Promise<void> => {
  await publicApiClient.post<ApiResponse<{ sent: boolean }>>(
    '/auth/forgot-password',
    { email },
  )
}

// postResetPassword consumes the one-time token from the email link and sets
// the new password. Public: the token IS the credential, and the link is
// usually opened in a browser with no session.
export const postResetPassword = async (
  token: string,
  password: string,
): Promise<void> => {
  await publicApiClient.post<ApiResponse<{ updated: boolean }>>(
    '/auth/reset-password',
    { token, password },
  )
}
