import { publicApiClient } from '@/shared/utils/apiConfig'

// Registration mirrors the backend's services.RegistrationState: how a
// sign-up is admitted right now. 'open' is the operator's open
// registration: a new organisation registers without any code.
export type Registration = 'first_owner' | 'open' | 'invite_code' | 'closed'

// SignInOptions mirrors GET /api/v1/server/sign-in-options. It is tokenless,
// because the sign-in and sign-up pages hold no token. `registration` is
// absent when the server could not read it; callers then keep the page as it
// always was. `single_sign_on` says whether the identity-provider routes
// (GET /auth/oidc/config and the sign-in flow) are served; an older backend
// does not send it.
export type SignInOptions = {
  email_delivery: boolean
  registration?: Registration
  // Why a 'closed' registration is closed, only where the page has something
  // to tell the visitor: 'email_unavailable' — the operator opened
  // registration, but this install cannot send the confirmation mail.
  registration_reason?: 'email_unavailable'
  single_sign_on?: boolean
}

type ApiResponse<T> = {
  success: boolean
  message: string
  data: T
}

// getSignInOptions answers null on any failure (an older backend without the
// route, a network error): null means "unknown", and every consumer falls
// back to the copy the page had before these signals existed.
export const getSignInOptions = async (): Promise<SignInOptions | null> => {
  try {
    const res = await publicApiClient.get<ApiResponse<SignInOptions>>(
      '/api/v1/server/sign-in-options',
    )
    return res.data.data ?? null
  } catch {
    return null
  }
}
