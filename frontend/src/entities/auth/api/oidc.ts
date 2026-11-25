import { apiClient, getApiUrl, publicApiClient } from '@/shared/utils/apiConfig'

import { markOidcFlowStarted } from '../oidcFlowMarker.ts'
import {
  ApiResponse,
  AuthResult,
  LoginChallenge,
  SignInResponse,
} from './postSignIn'

// OidcConfig mirrors GET /auth/oidc/config. `label` is the backend's legacy
// English button text; the SPA words its own from `provider`, the display
// name (empty when the install names none and the issuer is not Google).
// `issuer` tells this provider's linked identities from an earlier one's;
// `sign_up` is whether an unknown identity can create an account. The three
// newer fields are optional because an older backend does not send them.
export type OidcConfig = {
  enabled: boolean
  label: string
  provider?: string
  issuer?: string
  sign_up?: 'invite_code' | 'open' | 'closed'
}

// getOidcConfig reports whether OIDC login is available (the endpoint only
// exists when a provider was discovered). A 404/error means "disabled".
export const getOidcConfig = async (): Promise<OidcConfig | null> => {
  try {
    const res =
      await publicApiClient.get<ApiResponse<OidcConfig>>('/auth/oidc/config')
    return res.data.data
  } catch {
    return null
  }
}

export type LinkedIdentity = {
  // id is what makes an identity addressable for unlinking. Without it the
  // account page could show that Google was linked and had no way to name it
  // in a request — which is why there was no unlink at all.
  id: string
  provider: string
  email: string
  created_at: string
}

// getMyIdentities returns the current user's linked external identities.
export const getMyIdentities = async (): Promise<LinkedIdentity[]> => {
  const res = await apiClient.get<ApiResponse<LinkedIdentity[]>>(
    '/auth/oidc/identities',
  )
  return res.data.data ?? []
}

// startOidcLink begins linking the current account to the IdP: it sets the
// server-side link ticket cookie, then the browser navigates to /start so the
// callback attaches the verified identity to the signed-in user.
//
// The POST is behind a step-up, so it can answer 403 reauth_required. The
// global interceptor prompts and replays it, and only then does the navigation
// below run — which is why the redirect must stay AFTER the await.
//
// Rejecting means the user dismissed that dialog. Swallow it: not navigating is
// the correct outcome, and the interceptor deliberately shows no toast for a
// 403 it already handled, so there is nothing left to report.
export const startOidcLink = async (): Promise<void> => {
  try {
    await apiClient.post('/auth/oidc/link')
  } catch {
    return
  }
  window.location.assign(`${getApiUrl()}/auth/oidc/start`)
}

// unlinkOidcIdentity detaches one federated sign-in from the current account.
//
// The backend refuses with 409 when it would remove the user's last way in
// (password, passkeys and other identities all count; TOTP does not, because it
// cannot start a session). That message is written for the user, so it is shown
// verbatim rather than reworded here.
export const unlinkOidcIdentity = async (id: string): Promise<void> => {
  await apiClient.delete(`/auth/oidc/identities/${id}`)
}

// startOidcStepUp begins re-authentication at the IdP for the signed-in user.
//
// This is the only proof of presence a federated user with no local credential
// can make. It sets the server-side step-up cookie, then navigates: the backend
// asks the provider to re-authenticate the person rather than answer from the
// session they already have there, and the callback returns to /account with a
// one-time code.
//
// A full-page navigation, so the request that triggered the prompt cannot be
// resumed — the user confirms, comes back, and retries. That is the cost of a
// redirect-based proof, and it is why this is offered alongside the local
// methods rather than instead of them.
export const startOidcStepUp = async (): Promise<void> => {
  await apiClient.post('/auth/oidc/reauth')
  window.location.assign(`${getApiUrl()}/auth/oidc/start`)
}

// completeOidcStepUp exchanges the one-time code from /account?oidc_reauth=...
// for the confirmation ticket. Bound to the user who started the round trip.
export const completeOidcStepUp = async (
  code: string,
): Promise<TicketResponse> => {
  const res = await apiClient.post<ApiResponse<TicketResponse>>(
    '/auth/oidc/reauth/complete',
    { code },
  )
  return res.data.data
}

type TicketResponse = { ticket: string; expires_in: number }

// startOidcLogin navigates the browser to the backend OIDC start endpoint,
// which redirects to the IdP (e.g. Google). It is a full-page navigation, not
// a fetch, because it is a browser redirect flow. The /api prefix is stripped
// by the gateway to reach the backend /auth/oidc/start route.
export const startOidcLogin = (invite?: string) => {
  const base = `${getApiUrl()}/auth/oidc/start`
  const url = invite ? `${base}?invite=${encodeURIComponent(invite)}` : base
  // The callback consumes a handoff code only in a tab that started a flow.
  markOidcFlowStarted()
  window.location.assign(url)
}

// linkExistingAccount resolves the "an account with this email already exists"
// case: the backend holds the IdP-verified identity behind the one-time link
// code, and the password proves control of the local account. On success the
// identity is linked, the account's email is marked verified (so later Google
// logins are silent) and a normal login result comes back.
//
// The code is single-use: a wrong password burns it and the user restarts the
// Google round-trip.
export const linkExistingAccount = async (
  linkCode: string,
  password: string,
): Promise<SignInResponse> => {
  const response = await publicApiClient.post<
    ApiResponse<AuthResult | LoginChallenge>
  >(
    '/auth/oidc/link-existing',
    { link_code: linkCode, password },
    // Carries the cookies the sign-in round trip set, which is what ties the
    // code to the browser that started it.
    { withCredentials: true },
  )
  const data = response.data.data
  if ('pre_token' in data) {
    return { kind: 'challenge', ...data }
  }
  return { kind: 'tokens', ...data }
}

// completeOidcLogin exchanges the one-time handoff code (from the SPA callback
// URL) for the login result — tokens (single account) or an account-selection
// challenge (multi-account), same shape as password login.
export const completeOidcLogin = async (
  code: string,
): Promise<SignInResponse> => {
  const response = await publicApiClient.post<
    ApiResponse<AuthResult | LoginChallenge>
  >(
    '/auth/oidc/complete',
    { code },
    // Carries the cookies the sign-in round trip set, which is what ties the
    // code to the browser that started it.
    { withCredentials: true },
  )
  const data = response.data.data
  if ('pre_token' in data) {
    return { kind: 'challenge', ...data }
  }
  return { kind: 'tokens', ...data }
}
