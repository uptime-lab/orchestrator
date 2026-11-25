import axios, { type AxiosError, type InternalAxiosRequestConfig } from 'axios'
import { t } from 'i18next'

import { requestReauth } from '@/entities/auth/reauth/prompt'
import { getReauthTicket } from '@/entities/auth/reauth/ticket'

import { showError } from '@/shared/ui/toast'

import { markReauthCancelled } from './apiError'
import { getConfigValue } from './config'
import {
  EMAIL_VERIFICATION_REQUIRED,
  emailVerificationRedirect,
} from './emailVerificationGate'
import {
  isDefinitiveRefreshRefusal,
  RefreshTokenMissingError,
  retryTransient,
} from './refreshPolicy'
import {
  classifyIdentityChange,
  tokenIdentity,
  type IdentityChange,
} from './sessionIdentity'
import { withStorageLease } from './storageLease'

declare module 'axios' {
  interface AxiosRequestConfig {
    // Opt one request in or out of the global permission toast; see the 403
    // branch of the response interceptor.
    permissionToast?: 'always' | 'never'
  }
}

// REAUTH_TICKET_HEADER carries the step-up proof; REAUTH_REQUIRED_CODE is how
// the server says "you may do this, once you confirm" without a 401 — which
// would send the refresh path into a pointless loop and then sign the user out
// for trying to secure their account.
const REAUTH_TICKET_HEADER = 'X-Reauth-Ticket'
const REAUTH_REQUIRED_CODE = 'reauth_required'

export const getApiUrl = () => {
  const apiUrl = getConfigValue('VITE_API_URL')

  // In development, use /api prefix (Vite proxy will strip it)
  if (import.meta.env.MODE === 'development') {
    return '/api'
  }

  // In production, append /api if not already present
  if (apiUrl && !apiUrl.endsWith('/api')) {
    return apiUrl.endsWith('/') ? `${apiUrl}api` : `${apiUrl}/api`
  }

  return apiUrl || '/api'
}

export const getWebsocketUrl = () => {
  const websocketConfigUrl = getConfigValue('VITE_WEBSOCKET_URL')
  if (websocketConfigUrl?.startsWith('/')) {
    // Match the page's transport: use wss on HTTPS pages so the browser does not
    // block the WebSocket as mixed content, ws otherwise.
    const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
    return `${scheme}://${window.location.host}${websocketConfigUrl}`
  }
  return websocketConfigUrl
}

export const authToken = {
  getAccessToken: () => localStorage.getItem('access_token'),
  getRefreshToken: () => localStorage.getItem('refresh_token'),
  setTokens: (accessToken: string, refreshToken: string) => {
    localStorage.setItem('access_token', accessToken)
    localStorage.setItem('refresh_token', refreshToken)
  },
  clear: () => {
    localStorage.removeItem('access_token')
    localStorage.removeItem('refresh_token')
  },
  // Legacy support - for backward compatibility
  get: () => authToken.getAccessToken(),
  set: (token: string) => {
    const refreshToken = authToken.getRefreshToken() || ''
    authToken.setTokens(token, refreshToken)
  },
  parse: (token: string): { email: string; exp: number; user_id: string } => {
    const base64Url = token.split('.')[1]
    const base64 = base64Url.replace(/-/g, '+').replace(/_/g, '/')
    const jsonPayload = decodeURIComponent(
      window
        .atob(base64)
        .split('')
        .map((c) => `%${`00${c.charCodeAt(0).toString(16)}`.slice(-2)}`)
        .join(''),
    )

    return JSON.parse(jsonPayload)
  },
}

// Treat a token that expires within this window as already stale, so we refresh
// slightly ahead of expiry instead of racing it.
const TOKEN_EXPIRY_SKEW_MS = 60_000

const accessTokenExpiresAt = (): number | null => {
  const token = authToken.getAccessToken()
  if (!token) return null
  try {
    return authToken.parse(token).exp * 1000
  } catch {
    return null
  }
}

// Called when the session cannot be recovered, so the app can actually SIGN OUT
// instead of sitting in a half state.
//
// A callback rather than an import of the auth model, because that model imports
// this file — and the bug being fixed is bad enough to not want a lazy dynamic
// import in its path. The auth model registers the handler at module init.
//
// What went wrong without it: a failed refresh cleared the tokens and stopped
// there. $authState stayed LoggedIn, so no route guard fired, and every later
// request threw in the REQUEST interceptor ("No token provided") — which skips
// the response interceptor and therefore the error toast. The app froze into a
// stale, unclickable shell until the user reloaded by hand. Reachable with two
// open tabs, because the single-flight guard is per module and refresh tokens
// rotate on use.
let sessionExpiredHandler: (() => void) | null = null

export const setSessionExpiredHandler = (handler: () => void): void => {
  sessionExpiredHandler = handler
}

// Fired at most once per dead session: signing out is idempotent, but the toast
// and the redirect it triggers are not worth repeating per in-flight request.
let sessionExpiredReported = false

const reportSessionExpired = (): void => {
  authToken.clear()
  if (sessionExpiredReported) return
  sessionExpiredReported = true
  sessionExpiredHandler?.()
}

// Reset when a session is established again, so a later expiry is reported.
export const noteSessionEstablished = (): void => {
  sessionExpiredReported = false
}

// The identity (user and account) this tab signed in as. The token pair is
// shared by every tab through localStorage; this pin is not. See
// sessionIdentity.ts.
let pinnedIdentity: string | null = null

export const pinSessionIdentity = (): void => {
  pinnedIdentity = tokenIdentity(authToken.getAccessToken())
}

export const clearSessionIdentity = (): void => {
  pinnedIdentity = null
}

// How the stored token relates to the identity this tab signed in as.
export const storedIdentityChange = (): IdentityChange =>
  classifyIdentityChange(pinnedIdentity, authToken.getAccessToken())

// Told when another tab replaced or removed the session this tab runs on. A
// callback for the same reason as the session-expired handler above.
let identityChangedHandler:
  ((change: Exclude<IdentityChange, 'same'>) => void) | null = null

export const setIdentityChangedHandler = (
  handler: (change: Exclude<IdentityChange, 'same'>) => void,
): void => {
  identityChangedHandler = handler
}

// True (and the handler told) when this tab must not act with the stored
// token, because it belongs to a session other than the one the tab shows.
const sessionMovedElsewhere = (): boolean => {
  const change = storedIdentityChange()
  if (change === 'same') return false
  identityChangedHandler?.(change)
  return true
}

// Single-flight token refresh shared by the axios 401 interceptor and the
// WebSocket auth path, so a refresh never runs twice concurrently — the refresh
// token is rotated on use, and two parallel refreshes would invalidate each other.
let refreshInFlight: Promise<string> | null = null

// A non-stale access token from storage, or null. Used inside the cross-tab lock
// to notice that ANOTHER tab already refreshed while we waited for the lock.
const freshStoredAccessToken = (): string | null => {
  const expiresAt = accessTokenExpiresAt()
  if (expiresAt !== null && expiresAt - Date.now() > TOKEN_EXPIRY_SKEW_MS) {
    return authToken.getAccessToken()
  }
  return null
}

// refreshTokenSeen is the refresh token this tab held when it decided to
// refresh, read BEFORE waiting for the cross-tab lock.
const performTokenRefresh = async (
  refreshTokenSeen: string | null,
): Promise<string> => {
  // Held under the cross-tab lock: another tab may have refreshed while we
  // waited, so use its fresh token rather than spending our now-rotated refresh
  // token a second time (which is what the backend's reuse detection punishes).
  const alreadyFresh = freshStoredAccessToken()
  if (alreadyFresh) {
    return alreadyFresh
  }
  // A refresh token that changed while we waited was rotated by another tab;
  // the access token it stored beside it is the one to use, even when the
  // server is what found the old one wanting.
  const storedAccess = authToken.getAccessToken()
  if (
    refreshTokenSeen &&
    storedAccess &&
    authToken.getRefreshToken() !== refreshTokenSeen
  ) {
    return storedAccess
  }
  // Imported lazily to avoid a circular import (auth api -> apiConfig).
  const { apiAuth } = await import('@/entities/auth/api')
  // A rate limit, a server error or a lost request says nothing about the
  // token: try again a little later rather than ending the session.
  const response = await retryTransient(() => {
    // Re-read on every attempt: storage is shared, and spending a token that
    // was already rotated is what reuse detection punishes.
    const refreshToken = authToken.getRefreshToken()
    if (!refreshToken) {
      throw new RefreshTokenMissingError()
    }
    return apiAuth.postRefreshToken({ refresh_token: refreshToken })
  })
  authToken.setTokens(response.access_token, response.refresh_token)
  return response.access_token
}

// Fallback cross-tab lock for pages without the Web Locks API. The lease
// outlives a normal refresh round trip with room to spare; past it, a waiter
// goes ahead, re-reading storage first.
const REFRESH_LEASE_KEY = 'orchestrator_token_refresh_lease'
const REFRESH_LEASE_TTL_MS = 15_000
const refreshLeaseOwner = `${Date.now().toString(36)}-${Math.random()
  .toString(36)
  .slice(2)}`

const runTokenRefresh = async (): Promise<string> => {
  // navigator.locks serializes the refresh across ALL tabs of this origin, not
  // just within one module. Without it, two tabs waking together (session
  // restore, wake-from-sleep) each refresh with the SAME rotating refresh token;
  // the backend's reuse detection then revokes the whole token family and signs
  // BOTH tabs out, and the loser overwrites the winner's fresh tokens in shared
  // localStorage. Inside the lock, performTokenRefresh re-reads
  // storage and reuses a token another tab just wrote instead of refreshing
  // again.
  //
  // The Web Locks API exists only in a secure context, so an install served
  // over plain HTTP has none; a localStorage lease stands in for it there.
  const refreshTokenSeen = authToken.getRefreshToken()
  if (typeof navigator !== 'undefined' && navigator.locks) {
    return navigator.locks.request('orchestrator-token-refresh', () =>
      performTokenRefresh(refreshTokenSeen),
    )
  }
  if (typeof localStorage !== 'undefined') {
    return withStorageLease(
      {
        storage: localStorage,
        key: REFRESH_LEASE_KEY,
        ownerId: refreshLeaseOwner,
        ttlMs: REFRESH_LEASE_TTL_MS,
        maxWaitMs: REFRESH_LEASE_TTL_MS + 5_000,
        settleMs: 50,
        pollMs: 100,
      },
      () => performTokenRefresh(refreshTokenSeen),
    )
  }
  return performTokenRefresh(refreshTokenSeen)
}

export const refreshAccessToken = (): Promise<string> => {
  if (!refreshInFlight) {
    refreshInFlight = runTokenRefresh().finally(() => {
      refreshInFlight = null
    })
  }
  return refreshInFlight
}

// Return a non-expired access token, refreshing first if it is missing or about
// to expire. Returns null when no valid token can be obtained (e.g. the refresh
// token itself expired) so callers can stop instead of looping on a dead token.
// Throws when the refresh failed for a reason that says nothing about the
// session (rate limit, server error, network), so the caller can retry later.
export const ensureFreshAccessToken = async (): Promise<string | null> => {
  if (sessionMovedElsewhere()) {
    return null
  }
  const expiresAt = accessTokenExpiresAt()
  if (expiresAt !== null && expiresAt - Date.now() > TOKEN_EXPIRY_SKEW_MS) {
    return authToken.getAccessToken()
  }
  try {
    return await refreshAccessToken()
  } catch (error) {
    if (!isDefinitiveRefreshRefusal(error)) {
      throw error
    }
    // The WebSocket auth path comes through here. Its failure is the same dead
    // session as the axios one, and it used to end in a socket closed
    // "by plan" that is never retried — indistinguishable from a deliberate
    // sign-out, with the user still looking at an app that believed it was
    // logged in.
    reportSessionExpired()
    return null
  }
}

export const publicApiClient = axios.create({
  baseURL: getApiUrl(),
  timeout: 10 * 60 * 1000, // 10 minutes
  adapter: undefined,
  headers: {
    'Cache-Control': 'no-cache',
    Pragma: 'no-cache',
  },
})

export const apiClient = axios.create({
  baseURL: getApiUrl(),
  timeout: 10 * 60 * 1000, // 10 minutes
  adapter: undefined,
  headers: {
    'Cache-Control': 'no-cache',
    Pragma: 'no-cache',
  },
})

apiClient.interceptors.request.use(
  (config) => {
    if (getConfigValue('VITE_ESSENTIALS_API_ONLY') === 'on') {
      return config
    }

    // Never send a request as a session other than the one this tab shows:
    // another tab may have switched account, signed in as someone else or
    // signed out under it.
    if (sessionMovedElsewhere()) {
      throw new Error('The session changed in another tab.')
    }

    const accessToken = authToken.getAccessToken()
    if (!accessToken) {
      throw new Error('No token provided.')
    }

    // Use Bearer token format
    config.headers.Authorization = `Bearer ${accessToken}`

    // Ride along with the step-up confirmation while it is live, so the routes
    // that demand one are unlocked without every call site knowing they exist.
    const reauthTicket = getReauthTicket()
    if (reauthTicket) {
      config.headers[REAUTH_TICKET_HEADER] = reauthTicket
    }
    return config
  },
  (error) => Promise.reject(error),
)

apiClient.interceptors.response.use(
  (response) => response,
  async (error: AxiosError) => {
    const originalRequest = error.config as InternalAxiosRequestConfig & {
      _retry?: boolean
      _reauthRetry?: boolean
      // Opt in or out of the permission toast for one request; see the 403
      // branch below for why the default is method-based.
      permissionToast?: 'always' | 'never'
    }

    // On a 401, refresh the access token once (shared single-flight — concurrent
    // 401s await the same refresh) and retry the original request.
    if (error.response?.status === 401 && !originalRequest._retry) {
      originalRequest._retry = true
      try {
        const accessToken = await refreshAccessToken()
        if (originalRequest.headers) {
          originalRequest.headers.Authorization = `Bearer ${accessToken}`
        }
        return apiClient(originalRequest)
      } catch (refreshError) {
        // Only a definitive refusal ends the session. Anything else rejects
        // this request and keeps the tokens for the next one to try again.
        if (isDefinitiveRefreshRefusal(refreshError)) {
          reportSessionExpired()
        }
        return Promise.reject(refreshError)
      }
    }

    // The account demands a second factor this user has not enrolled. The
    // server already restricts them to the enrollment endpoints; sending them
    // to the page that hosts those endpoints is what turns a wall of 403s into
    // a flow. Not a 401 on purpose — the session IS valid, so refreshing the
    // token would loop forever.
    const body = error.response?.data as
      { message?: string; code?: string } | undefined
    const message = body?.message
    if (
      error.response?.status === 403 &&
      message === 'two_factor_enrollment_required' &&
      !window.location.pathname.startsWith('/account')
    ) {
      window.location.assign('/account?tab=account&enroll_2fa=1')
    }

    // The account was created through open registration and its owner has
    // not confirmed the address: the server allows only the confirmation
    // screen, so that is where the browser goes.
    const confirmAt = emailVerificationRedirect(
      error.response?.status,
      message,
      window.location.pathname,
    )
    if (confirmAt) {
      window.location.assign(confirmAt)
    }

    // The route needs a fresh proof of identity. Prompt for it and replay the
    // request, so changing a credential is one uninterrupted flow rather than
    // an error the user has to interpret and restart.
    //
    // Retried at most once (_reauthRetry): if the replay is refused again the
    // ticket is not the problem, and looping would trap the user in a dialog.
    if (
      error.response?.status === 403 &&
      body?.code === REAUTH_REQUIRED_CODE &&
      !originalRequest._reauthRetry
    ) {
      originalRequest._reauthRetry = true
      const confirmed = await requestReauth()
      if (confirmed) {
        const ticket = getReauthTicket()
        if (ticket && originalRequest.headers) {
          originalRequest.headers[REAUTH_TICKET_HEADER] = ticket
        }
        return apiClient(originalRequest)
      }
      // Dismissed: the user changed their mind, which is not an error to
      // show under the control that asked.
      markReauthCancelled(error)
      return Promise.reject(error)
    }

    // Every other 403 used to fall through to a bare reject, so a permission
    // denial was indistinguishable from the app ignoring the click — which is
    // what a viewer met on Add Node, the whole Workflows page and every
    // drag-to-assign on the OU rail. Said out loud once, here, rather than at
    // twenty call sites that would each have to remember.
    //
    // Skipped for the two 403s that are already handled above (they carry their
    // own machine-readable codes and are retried, not reported), and for
    // capability probes that are expected to be refused for some roles.
    if (error.response?.status === 403 && !originalRequest?._reauthRetry) {
      const data = error.response.data as
        { message?: string; error?: string; code?: string } | undefined
      // Only for a request the user ASKED for, which in practice means a
      // mutating method.
      //
      // The toast was added to end the silence — a viewer clicking Add Node
      // or dragging onto the OU rail and nothing happening — and as first written
      // it fired on any 403 at all. That includes page LOADERS: Secrets is an
      // unconditional nav entry whose loader fetches two secret.read endpoints
      // with Promise.all, and Credentials fetches two access.manage endpoints on
      // mount and again on every accessChanged. Both are admin-only in the
      // default matrix, so every non-admin got a non-dismissing error card for
      // merely navigating — "silently does nothing" traded for "accuses you of
      // doing something wrong". A GET refused by the server is the UI failing to
      // gate a page; the honest place to fix that is the nav,
      // not a toast the user cannot act on.
      //
      // `permissionToast: 'always'` is the escape hatch for a GET that IS a
      // deliberate click, e.g. revealing a secret value.
      const method = (originalRequest?.method ?? 'get').toLowerCase()
      const userInitiated =
        method !== 'get' && method !== 'head' && method !== 'options'
      const override = originalRequest?.permissionToast
      const shouldReport =
        override === 'always' || (override !== 'never' && userInitiated)
      if (!shouldReport) {
        return Promise.reject(error)
      }
      // Match on BOTH fields. The two handled refusals are signalled
      // differently: reauth uses SendErrorWithCode (populates `code`), while
      // the 2FA-enrollment refusal uses SendError, which populates `message`
      // and leaves `code` empty — which is why the redirect above tests
      // `message`. Checking only `code` therefore never matched the 2FA case,
      // and told a user in the middle of a MANDATORY enrollment flow that they
      // lack permission, on every blocked request, with a toast that does not
      // auto-dismiss.
      const marker = data?.code ?? data?.message
      if (
        marker !== 'two_factor_enrollment_required' &&
        marker !== EMAIL_VERIFICATION_REQUIRED &&
        marker !== REAUTH_REQUIRED_CODE
      ) {
        showError(
          t('shared.utils.api.permissionDenied'),
          data?.message ?? data?.error ?? undefined,
        )
      }
    }

    return Promise.reject(error)
  },
)
