import { publicApiClient } from '@/shared/utils/apiConfig'

import { AuthResult, type ApiResponse } from './postSignIn'

export type RegisterPayload = {
  email: string
  password: string
  name: string
  invite_code?: string
  // Sent only when the install has terms of use and the user agreed to them.
  terms_accepted?: boolean
}

// SignUpOutcome is what a sign-up produced: a session, or — for an open
// registration — nothing but "confirm your address from the mail". The open
// path answers the same for an address that already has an account, so the
// page shows one message for both.
export type SignUpOutcome =
  | { kind: 'session'; result: AuthResult }
  | { kind: 'check_email' }

type PendingResult = { verification_pending?: boolean }

// signUpOutcome reads the backend's answer: 202 with verification_pending is
// an open sign-up waiting for its confirmation; anything else carries tokens.
export const signUpOutcome = (
  status: number,
  data: AuthResult | PendingResult | null | undefined,
): SignUpOutcome =>
  status === 202 || (data as PendingResult | null)?.verification_pending
    ? { kind: 'check_email' }
    : { kind: 'session', result: data as AuthResult }

export const postSignUp = async (
  payload: RegisterPayload,
): Promise<SignUpOutcome> => {
  const response = await publicApiClient.post<
    ApiResponse<AuthResult | PendingResult>
  >('/auth/signup', payload)
  return signUpOutcome(response.status, response.data.data)
}
