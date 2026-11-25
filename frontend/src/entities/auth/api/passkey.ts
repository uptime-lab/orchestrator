import { t } from 'i18next'

import { apiClient, publicApiClient } from '@/shared/utils/apiConfig'

import { ApiResponse, AuthResult, LoginChallenge } from './postSignIn'

export type Passkey = {
  id: string
  name: string
  synced: boolean
  created_at: string
  last_used_at?: string
}

// WebAuthn speaks ArrayBuffers; JSON speaks base64url. These two helpers are
// the whole impedance mismatch, and getting the URL-safe alphabet wrong is the
// classic way passkey integrations fail on a subset of credentials.

export const base64UrlToBuffer = (value: string): ArrayBuffer => {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/')
  const binary = atob(padded.padEnd(Math.ceil(padded.length / 4) * 4, '='))
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i)
  return bytes.buffer
}

export const bufferToBase64Url = (buffer: ArrayBuffer): string => {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (let i = 0; i < bytes.length; i += 1)
    binary += String.fromCharCode(bytes[i])
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// isPasskeySupported reports whether this browser can do WebAuthn at all, so
// the UI can hide a button that could only ever fail.
export const isPasskeySupported = (): boolean =>
  typeof window !== 'undefined' &&
  !!window.PublicKeyCredential &&
  !!navigator.credentials

type BeginResponse = {
  ticket: string
  options: Record<string, unknown>
}

// listPasskeys returns the signed-in user's credentials.
export const listPasskeys = async (): Promise<Passkey[]> => {
  const res = await apiClient.get<ApiResponse<Passkey[]>>(
    '/api/v1/users/me/passkeys',
  )
  return res.data.data
}

// registerPasskey runs the full creation ceremony and stores the credential.
export const registerPasskey = async (name: string): Promise<Passkey> => {
  const begin = await apiClient.post<ApiResponse<BeginResponse>>(
    '/api/v1/users/me/passkeys/register/begin',
  )
  const { ticket, options } = begin.data.data
  const publicKey = options as unknown as PublicKeyCredentialCreationOptions & {
    challenge: string
    user: { id: string }
    excludeCredentials?: { id: string }[]
  }

  const credential = (await navigator.credentials.create({
    publicKey: {
      ...publicKey,
      challenge: base64UrlToBuffer(publicKey.challenge as unknown as string),
      user: {
        ...publicKey.user,
        id: base64UrlToBuffer(publicKey.user.id as unknown as string),
      },
      excludeCredentials: (publicKey.excludeCredentials ?? []).map((c) => ({
        ...c,
        id: base64UrlToBuffer(c.id as unknown as string),
      })),
    } as PublicKeyCredentialCreationOptions,
  })) as PublicKeyCredential | null

  if (!credential) {
    throw new Error(t('entities.auth.passkey.createCancelled'))
  }

  const attestation = credential.response as AuthenticatorAttestationResponse
  // Posted as raw WebAuthn JSON: the server hands the body straight to the
  // library, which re-verifies the signed attestation. Wrapping or re-encoding
  // it would break that signature.
  const res = await apiClient.post<ApiResponse<Passkey>>(
    `/api/v1/users/me/passkeys/register/finish?ticket=${encodeURIComponent(ticket)}&name=${encodeURIComponent(name)}`,
    {
      id: credential.id,
      rawId: bufferToBase64Url(credential.rawId),
      type: credential.type,
      response: {
        clientDataJSON: bufferToBase64Url(attestation.clientDataJSON),
        attestationObject: bufferToBase64Url(attestation.attestationObject),
      },
    },
  )
  return res.data.data
}

// deletePasskey removes one credential.
export const deletePasskey = async (id: string): Promise<void> => {
  await apiClient.delete(`/api/v1/users/me/passkeys/${id}`)
}

// loginWithPasskey runs the usernameless authentication ceremony.
//
// No email is collected first: the authenticator itself resolves which account
// to offer, which is what makes this a complete sign-in rather than a second
// step after a password.
export const loginWithPasskey = async (): Promise<
  | { kind: 'tokens'; result: AuthResult }
  | { kind: 'challenge'; challenge: LoginChallenge }
> => {
  const begin = await publicApiClient.post<ApiResponse<BeginResponse>>(
    '/auth/passkey/begin',
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
    throw new Error(t('entities.auth.passkey.signInCancelled'))
  }

  const response = assertion.response as AuthenticatorAssertionResponse
  const res = await publicApiClient.post<
    ApiResponse<AuthResult | LoginChallenge>
  >(`/auth/passkey/finish?ticket=${encodeURIComponent(ticket)}`, {
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
  })

  const data = res.data.data
  if ('pre_token' in data) {
    return { kind: 'challenge', challenge: data }
  }
  return { kind: 'tokens', result: data }
}
