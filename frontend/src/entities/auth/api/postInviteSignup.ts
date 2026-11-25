import { publicApiClient } from '@/shared/utils/apiConfig'

import { ApiResponse, AuthResult } from './postSignIn'

export type InviteSignupPayload = {
  token: string
  name: string
  password: string
}

// postInviteSignup creates a new user from an invite (no account of their
// own — only the membership the invite grants) and returns a token pair.
export const postInviteSignup = async (
  payload: InviteSignupPayload,
): Promise<AuthResult> => {
  const response = await publicApiClient.post<ApiResponse<AuthResult>>(
    '/auth/invite-signup',
    payload,
  )
  return response.data.data
}
