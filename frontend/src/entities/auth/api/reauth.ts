import { t } from 'i18next'

import { apiClient } from '@/shared/utils/apiConfig'

import { setReauthTicket } from '../reauth/ticket'
import { base64UrlToBuffer, bufferToBase64Url } from './passkey'
import { ApiResponse } from './postSignIn'

// Which credentials the user can confirm their identity with. `required` is
// false for a user who holds none — they are not asked to confirm at all,
// because there is nothing they could present.
export type ReauthMethods = {
  password: boolean
  totp: boolean
  passkey: boolean
  // oidc means the user can be sent back to their identity provider. For a
  // federated user with no local credential it is the only proof available —
  // and until it existed, that user was not asked to confirm at all.
  oidc: boolean
  required: boolean
}

type TicketResponse = {
  ticket: string
  expires_in: number
}

type BeginResponse = {
  ticket: string
  options: Record<string, unknown>
}

export const getReauthMethods = async (): Promise<ReauthMethods> => {
  const res = await apiClient.get<ApiResponse<ReauthMethods>>(
    '/api/v1/users/me/reauth/methods',
  )
  return res.data.data
}

// store keeps the freshly minted ticket where the request interceptor finds it,
// so callers never have to thread it through.
const store = (response: TicketResponse): void => {
  setReauthTicket(response.ticket, response.expires_in)
}

// storeStepUpTicket puts a ticket minted by the IdP round trip where the
// request interceptor finds it, exactly like the local methods do.
export const storeStepUpTicket = (ticket: string, expiresIn: number): void => {
  setReauthTicket(ticket, expiresIn)
}

export const confirmWithPassword = async (password: string): Promise<void> => {
  const res = await apiClient.post<ApiResponse<TicketResponse>>(
    '/api/v1/users/me/reauth/password',
    { password },
  )
  store(res.data.data)
}

export const confirmWithTotp = async (code: string): Promise<void> => {
  const res = await apiClient.post<ApiResponse<TicketResponse>>(
    '/api/v1/users/me/reauth/totp',
    { code },
  )
  store(res.data.data)
}

// confirmWithPasskey runs a WebAuthn assertion purely as proof of presence —
// the same ceremony as signing in, but the result is an elevation of the
// session that already exists rather than a new one.
export const confirmWithPasskey = async (): Promise<void> => {
  const begin = await apiClient.post<ApiResponse<BeginResponse>>(
    '/api/v1/users/me/reauth/passkey/begin',
  )
  const { ticket, options } = begin.data.data
  const publicKey = options as unknown as PublicKeyCredentialRequestOptions & {
    challenge: string
    allowCredentials?: { id: string }[]
  }

  const assertion = (await navigator.credentials.get({
    publicKey: {
      ...publicKey,
      challenge: base64UrlToBuffer(publicKey.challenge as unknown as string),
      allowCredentials: (publicKey.allowCredentials ?? []).map((c) => ({
        ...c,
        id: base64UrlToBuffer(c.id as unknown as string),
      })),
    } as PublicKeyCredentialRequestOptions,
  })) as PublicKeyCredential | null

  if (!assertion) {
    throw new Error(t('entities.auth.reauth.cancelled'))
  }

  const response = assertion.response as AuthenticatorAssertionResponse
  // Raw WebAuthn JSON, for the same reason as the login ceremony: the server
  // hands the body to the library that re-verifies the signature over it.
  const res = await apiClient.post<ApiResponse<TicketResponse>>(
    `/api/v1/users/me/reauth/passkey/finish?ticket=${encodeURIComponent(ticket)}`,
    {
      id: assertion.id,
      rawId: bufferToBase64Url(assertion.rawId),
      type: assertion.type,
      response: {
        clientDataJSON: bufferToBase64Url(response.clientDataJSON),
        authenticatorData: bufferToBase64Url(response.authenticatorData),
        signature: bufferToBase64Url(response.signature),
        userHandle: response.userHandle
          ? bufferToBase64Url(response.userHandle)
          : null,
      },
    },
  )
  store(res.data.data)
}
