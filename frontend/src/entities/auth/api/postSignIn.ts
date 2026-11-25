import { publicApiClient } from '@/shared/utils/apiConfig'

export type SignInPayload = {
  email: string
  password: string
}

export type AuthResult = {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
  user: {
    id: string
    email: string
    name: string
    created_at: string
    updated_at: string
  }
}

export type AccountOption = {
  id: string
  name: string
  // require_two_factor: this account demands a second factor from every member.
  require_two_factor: boolean
}

// LoginChallenge is returned for multi-account users: no tokens yet, a
// short-lived pre-auth token plus the accounts to choose from.
export type LoginChallenge = {
  pre_token: string
  accounts: AccountOption[]
}

// TwoFactorMethod is one way a second-factor challenge can be completed:
// a code from the authenticator app (or a recovery code), or the ordinary
// passkey sign-in, which is itself two factors.
export type TwoFactorMethod = 'totp' | 'passkey'

// TwoFactorChallenge is returned when the password was right but a second
// factor is still owed. It carries the ticket and the enrolled factor KINDS:
// no accounts, no profile — a stolen password must not even reveal which
// accounts the victim belongs to. Older servers omit the methods; the form
// then assumes a code is expected, which is what those servers accept.
export type TwoFactorChallenge = {
  two_factor_token: string
  two_factor_methods?: TwoFactorMethod[]
}

// SignInResponse is a discriminated union distinguished by `kind`.
export type SignInResponse =
  | ({ kind: 'tokens' } & AuthResult)
  | ({ kind: 'challenge' } & LoginChallenge)
  | ({ kind: 'two_factor' } & TwoFactorChallenge)

export type ApiResponse<T> = {
  success: boolean
  message: string
  data: T
  timestamp: string
}

export const postSignIn = async (
  payload: SignInPayload,
): Promise<SignInResponse> => {
  const response = await publicApiClient.post<
    ApiResponse<AuthResult | LoginChallenge | TwoFactorChallenge>
  >('/auth/login', payload)
  const data = response.data.data
  // Order matters: a two-factor response has neither tokens nor a pre_token.
  if ('two_factor_token' in data) {
    return { kind: 'two_factor', ...data }
  }
  if ('pre_token' in data) {
    return { kind: 'challenge', ...data }
  }
  return { kind: 'tokens', ...data }
}
