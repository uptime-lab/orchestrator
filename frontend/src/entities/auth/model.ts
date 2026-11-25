import type { AxiosError } from 'axios'
import { createEffect, createEvent, createStore, sample } from 'effector'
import { t } from 'i18next'

import { apiAuth } from '@/entities/auth/api'
import { AuthState, AuthStatus, SignInSuccess } from '@/entities/auth/types.ts'
import { apiUsers } from '@/entities/user/api'
import { mapApiUserToEntity } from '@/entities/user/api/mapApiUserToEntity'
import { UserEntity } from '@/entities/user/api/types.ts'
import { userMock } from '@/entities/user/mocks.ts'

import {
  authToken,
  clearSessionIdentity,
  noteSessionEstablished,
  pinSessionIdentity,
  setIdentityChangedHandler,
  setSessionExpiredHandler,
  storedIdentityChange,
} from '@/shared/utils/apiConfig.ts'
import { getConfigValue } from '@/shared/utils/config.ts'
import { isDefinitiveRefreshRefusal } from '@/shared/utils/refreshPolicy.ts'

import {
  LoginChallenge,
  SignInPayload,
  SignInResponse,
  TwoFactorMethod,
} from './api/postSignIn.ts'
import { RegisterPayload } from './api/postSignUp.ts'
import {
  bootFailureAction,
  bootRetryDelay,
  NO_TOKENS_MESSAGE,
} from './bootPolicy.ts'
import { clearReauthTicket } from './reauth/ticket'
import { normalizeTwoFactorMethods } from './twoFactorMethods.ts'
import {
  AUTH_LOCAL_STORAGE_EMAIL_KEY,
  delay,
  isAxiosErrorBadAuth,
} from './utils.ts'

// SESSION_EXPIRED_NOTICE_KEY marks a forced sign-out (refresh token dead) so
// the sign-in page can say WHY the user landed there. Read-and-cleared by
// consumeSessionExpiredNotice below.
export const SESSION_EXPIRED_NOTICE_KEY = 'orchestrator_session_expired'

// consumeSessionExpiredNotice reports whether the last sign-out was a session
// expiry, clearing the flag so the notice shows once.
export const consumeSessionExpiredNotice = (): boolean => {
  try {
    const set = sessionStorage.getItem(SESSION_EXPIRED_NOTICE_KEY) === '1'
    if (set) sessionStorage.removeItem(SESSION_EXPIRED_NOTICE_KEY)
    return set
  } catch {
    return false
  }
}

// ============= Store =============

export const DEFAULT_AUTH_STATE: AuthState =
  getConfigValue('VITE_ESSENTIALS_API_ONLY') === 'on'
    ? { status: AuthStatus.LoggedIn, user: userMock }
    : { status: AuthStatus.Idle }
export const $authState = createStore<AuthState>(DEFAULT_AUTH_STATE)

// ============= Events =============

const signIn = createEvent<{ user: UserEntity }>()
const signOut = createEvent()
const lockOut = createEvent()
// Another tab of this browser replaced this tab's session with a different
// user or account. The tab keeps its screen but sends nothing until reloaded.
const sessionSwitchedElsewhere = createEvent()
// The signed-in user's own profile changed (their display name). Replaces the
// user in place; unlike signIn it starts nothing that a sign-in starts.
const currentUserUpdated = createEvent<{ user: UserEntity }>()

export const events = {
  signIn,
  signOut,
  lockOut,
  sessionSwitchedElsewhere,
  currentUserUpdated,
}

// ============= Effects =============

/**
 * Initialize authentication from stored in local storage token
 */
export const initAuthFx = createEffect<void, SignInSuccess, AxiosError>(
  async () => {
    const accessToken = authToken.getAccessToken()
    const refreshToken = authToken.getRefreshToken()

    // If no tokens at all, user is not logged in
    if (!accessToken && !refreshToken) {
      throw new Error(NO_TOKENS_MESSAGE)
    }

    // If we have a refresh token but no access token, try to refresh
    if (!accessToken && refreshToken) {
      try {
        const refreshResult = await apiAuth.postRefreshToken({
          refresh_token: refreshToken,
        })
        authToken.setTokens(
          refreshResult.access_token,
          refreshResult.refresh_token,
        )
        // Continue to get user with new access token
      } catch (refreshError) {
        // Only a refused token is dropped; a transient failure keeps it for
        // the next attempt.
        if (isDefinitiveRefreshRefusal(refreshError)) {
          authToken.clear()
        }
        throw refreshError
      }
    }

    // Get user with access token
    const user = await apiUsers.getCurrentUser()
    return { user }
  },
)

/**
 * Authenticate user with credentials - and try 5 attempts in case the error is not "bad credentials"
 */
// SignInResult is a discriminated union: a logged-in user (single account),
// an account-selection challenge (multiple accounts), or a second-factor
// challenge (the password was right but a code is still owed).
export type SignInResult =
  | { user: UserEntity }
  | { challenge: LoginChallenge }
  | { twoFactor: TwoFactorChallengeState }

// TwoFactorChallengeState is the pending second-factor step: the ticket plus
// the ways it can be completed. A server that names no methods gets the code
// form, the only completion such a server accepts.
export type TwoFactorChallengeState = {
  token: string
  methods: TwoFactorMethod[]
}

export const signInAuthFx = createEffect<
  SignInPayload,
  SignInResult,
  AxiosError
>(async (payload) => {
  let result: SignInResponse | null = null
  let success = false
  let attempts = 0

  // Retry logic for server setup
  while (!success && attempts < 5) {
    try {
      result = await apiAuth.postSignIn(payload)
      success = true
    } catch (e) {
      attempts++
      // Retry only errors that look like the server is not ready yet
      // (network failure / 5xx); a definitive 4xx answer is thrown as is.
      const status = (e as AxiosError).response?.status
      const isClientError =
        status !== undefined && status >= 400 && status < 500
      if (
        !isClientError &&
        !isAxiosErrorBadAuth(e as AxiosError) &&
        attempts < 5
      ) {
        await delay(5000)
      } else {
        throw e
      }
    }
  }

  if (!result) {
    throw new Error(t('entities.auth.model.authenticationFailed'))
  }

  localStorage.setItem(AUTH_LOCAL_STORAGE_EMAIL_KEY, payload.email)

  // A second factor is owed: nothing is stored yet, not even a pre-auth
  // token. Checked before the account challenge because this response carries
  // neither tokens nor an account list.
  if (result.kind === 'two_factor') {
    return {
      twoFactor: {
        token: result.two_factor_token,
        methods: normalizeTwoFactorMethods(result.two_factor_methods),
      },
    }
  }

  // Multi-account user: surface the challenge, do not log in yet.
  if (result.kind === 'challenge') {
    return {
      challenge: { pre_token: result.pre_token, accounts: result.accounts },
    }
  }

  authToken.setTokens(result.access_token, result.refresh_token)
  return { user: mapApiUserToEntity(result.user) }
})

// passkeyLoginFx runs the usernameless WebAuthn ceremony and logs the user in.
// Like every other login path it can still land on account selection.
export const passkeyLoginFx = createEffect<void, SignInResult, AxiosError>(
  async () => {
    const result = await apiAuth.loginWithPasskey()
    if (result.kind === 'challenge') {
      return {
        challenge: {
          pre_token: result.challenge.pre_token,
          accounts: result.challenge.accounts,
        },
      }
    }
    authToken.setTokens(result.result.access_token, result.result.refresh_token)
    return { user: mapApiUserToEntity(result.result.user) }
  },
)

// completeTwoFactorFx exchanges the ticket plus a code for a session. It can
// still land on the account-selection challenge, so it reuses SignInResult.
export const completeTwoFactorFx = createEffect<
  { twoFactorToken: string; code: string },
  SignInResult,
  AxiosError
>(async ({ twoFactorToken, code }) => {
  const result = await apiAuth.postCompleteTwoFactor(twoFactorToken, code)
  if (result.kind === 'challenge') {
    return {
      challenge: {
        pre_token: result.challenge.pre_token,
        accounts: result.challenge.accounts,
      },
    }
  }
  authToken.setTokens(result.result.access_token, result.result.refresh_token)
  return { user: mapApiUserToEntity(result.result.user) }
})

// selectAccountFx exchanges the pre-auth token for an account-pinned pair and
// logs the user in.
export const selectAccountFx = createEffect<
  { pre_token: string; account_id: string },
  SignInSuccess,
  AxiosError
>(async (payload) => {
  const authResult = await apiAuth.postSelectAccount(payload)
  authToken.setTokens(authResult.access_token, authResult.refresh_token)
  return { user: mapApiUserToEntity(authResult.user) }
})

// completeOidcFx exchanges the OIDC handoff code for a session: it either logs
// the user in (single account) or surfaces the account-selection challenge,
// reusing the same discriminated result as password login.
export const completeOidcFx = createEffect<string, SignInResult, AxiosError>(
  async (code) => {
    const result = await apiAuth.completeOidcLogin(code)
    // The federated paths never ask for a TOTP code — the identity provider
    // owns that account's MFA policy — but they share the response union, so
    // fail loudly rather than pretend a session exists.
    if (result.kind === 'two_factor') {
      throw new Error(t('entities.auth.model.unexpectedTwoFactor'))
    }
    if (result.kind === 'challenge') {
      return {
        challenge: { pre_token: result.pre_token, accounts: result.accounts },
      }
    }
    authToken.setTokens(result.access_token, result.refresh_token)
    return { user: mapApiUserToEntity(result.user) }
  },
)

// linkExistingAccountFx completes a Google login that collided with an existing
// local account, by proving control of that account with its password. Same
// discriminated result as any other login path.
export const linkExistingAccountFx = createEffect<
  { linkCode: string; password: string },
  SignInResult,
  AxiosError
>(async ({ linkCode, password }) => {
  const result = await apiAuth.linkExistingAccount(linkCode, password)
  // The federated paths never ask for a TOTP code — the identity provider
  // owns that account's MFA policy — but they share the response union, so
  // fail loudly rather than pretend a session exists.
  if (result.kind === 'two_factor') {
    throw new Error(t('entities.auth.model.unexpectedTwoFactor'))
  }
  if (result.kind === 'challenge') {
    return {
      challenge: { pre_token: result.pre_token, accounts: result.accounts },
    }
  }
  authToken.setTokens(result.access_token, result.refresh_token)
  return { user: mapApiUserToEntity(result.user) }
})

// SignUpResult: signed in, or (open registration) waiting for the owner to
// confirm their address — no session exists then.
export type SignUpResult = SignInSuccess | { checkEmail: true }

export const signUpAuthFx = createEffect<
  RegisterPayload,
  SignUpResult,
  AxiosError
>(async (payload) => {
  const outcome = await apiAuth.postSignUp(payload)
  if (outcome.kind === 'check_email') return { checkEmail: true }
  const authResult = outcome.result
  authToken.setTokens(authResult.access_token, authResult.refresh_token)
  localStorage.setItem(AUTH_LOCAL_STORAGE_EMAIL_KEY, payload.email)
  // Map API user to UserEntity
  const user = mapApiUserToEntity(authResult.user)
  return { user }
})

export const signUpAndLoginAuthFx = createEffect(
  async (payload: RegisterPayload) => {
    return await signUpAuthFx(payload)
  },
)

// ============= Derived stores =============

export const isSignedIn = $authState.map(
  (state) => state.status === AuthStatus.LoggedIn,
)
export const isSignedInString = isSignedIn.map((state) =>
  state ? 'signed' : 'unsigned',
)

// $loginChallenge holds the pending account-selection challenge (null when
// none). The /login page renders the account picker while it is set.
export const $loginChallenge = createStore<LoginChallenge | null>(null)

// ============= Side effects =============

// A step-up confirmation belongs to the person who made it. Dropping it on any
// session change stops an elevation from carrying over to whoever signs in
// next on this machine.
signOut.watch(() => clearReauthTicket())
signIn.watch(() => clearReauthTicket())

// A session that cannot be refreshed must actually END. apiConfig detects it (it
// owns the refresh) but must not import this model — this file imports apiConfig
// — so it calls back through a registered handler.
//
// Before this, a failed refresh cleared the tokens and stopped: $authState stayed
// LoggedIn, no route guard fired, and every later request threw in the axios
// REQUEST interceptor, which skips the response interceptor and therefore the
// error toast. The app froze into a stale, unclickable shell until the user
// reloaded by hand. The reactive guard in _mainLayout (useAuthOnlyRoute) already
// redirects on any status other than LoggedIn — it was simply never told.
//
// The flag makes the landing comprehensible: the redirect drops the user on the
// sign-in page with no explanation, which reads as "the app signed me out for
// no reason". sessionStorage, not a store — the page the notice
// renders on is outside the authenticated app, right after a state reset.
setSessionExpiredHandler(() => {
  try {
    sessionStorage.setItem(SESSION_EXPIRED_NOTICE_KEY, '1')
  } catch {
    // Storage can be unavailable (private mode quirks); the sign-out itself
    // must never depend on the notice.
  }
  signOut()
})
signIn.watch(() => noteSessionEstablished())

// Pin the identity this tab now shows, so a token another tab writes for a
// different user or account is noticed instead of silently used. Registered
// before the sign-out navigation below, so the pin is gone before any request
// the teardown makes.
signIn.watch(() => pinSessionIdentity())
signOut.watch(() => clearSessionIdentity())
// A lock drops the tokens on purpose; that is not another tab's doing.
lockOut.watch(() => clearSessionIdentity())

export const $sessionSwitchedElsewhere = createStore(false).on(
  sessionSwitchedElsewhere,
  () => true,
)

// A sign-out in another tab removed the tokens under this one: end this tab's
// session too, rather than leave it showing data it can no longer act on. A
// switch to another user or account cannot be followed in place — every store
// and cache holds the old account — so the tab is frozen behind a reload
// prompt instead.
setIdentityChangedHandler((change) => {
  if (change === 'signed-out') {
    signOut()
  } else {
    sessionSwitchedElsewhere()
  }
})

// The storage event fires in every OTHER tab when one writes localStorage,
// which is how a switch is noticed before this tab tries to use the token.
if (typeof window !== 'undefined') {
  window.addEventListener('storage', (event) => {
    if (event.key !== null && event.key !== 'access_token') return
    const change = storedIdentityChange()
    if (change === 'signed-out') signOut()
    else if (change === 'switched') sessionSwitchedElsewhere()
  })
}

$authState
  .on(signIn, (_, { user }) => ({ status: AuthStatus.LoggedIn, user }))
  .on(signOut, () => ({ status: AuthStatus.LoggedOut }))
  .on(currentUserUpdated, (state, { user }) =>
    state.status === AuthStatus.LoggedIn && state.user.id === user.id
      ? { ...state, user }
      : state,
  )
  .on(lockOut, (state) =>
    state.status !== AuthStatus.LoggedOut && state.status !== AuthStatus.Idle
      ? { ...state, status: AuthStatus.Locked }
      : state,
  )

$loginChallenge
  .on(passkeyLoginFx.doneData, (_, result) =>
    'challenge' in result ? result.challenge : null,
  )
  .on(signInAuthFx.doneData, (_, result) =>
    'challenge' in result ? result.challenge : null,
  )
  .on(completeOidcFx.doneData, (_, result) =>
    'challenge' in result ? result.challenge : null,
  )
  .on(linkExistingAccountFx.doneData, (_, result) =>
    'challenge' in result ? result.challenge : null,
  )
  .reset(signIn, signOut)

// $twoFactorChallenge holds the pending second-factor ticket. Separate from
// $loginChallenge: they are different steps and can occur in sequence (code
// first, then account selection).
export const $twoFactorChallenge = createStore<TwoFactorChallengeState | null>(
  null,
)
  .on(signInAuthFx.doneData, (_, result) =>
    'twoFactor' in result ? result.twoFactor : null,
  )
  .on(completeTwoFactorFx.doneData, () => null)
  // A passkey sign-in completes a passkey-method challenge: it is a full
  // login in its own right, so the pending step is simply over.
  .on(passkeyLoginFx.doneData, () => null)
  .reset(signIn, signOut)

// Revoke server-side BEFORE dropping the local copy — once cleared there is
// nothing left to revoke with. Failures are ignored: the user is leaving either
// way, and the local tokens are gone regardless.
const revokeSession = async (): Promise<void> => {
  const refreshToken = authToken.getRefreshToken()
  const revoked = refreshToken
    ? apiAuth.postLogout(refreshToken).catch(() => undefined)
    : Promise.resolve()
  authToken.clear()
  await revoked
}

lockOut.watch(() => {
  void revokeSession()
})

// Signing out has to leave nothing of the previous tenant in this document.
// The effector entity stores, the router's loader cache and the query cache are
// all account-scoped and none of them is dropped by an in-app navigation, so
// the next person to sign in on this browser was shown the previous user's
// nodes, secrets and playbooks — and kept them, because a weaker role's own
// refetch is refused and the loaders swallow that. A hard navigation
// reinitializes all three at once; it is the same move, for the same reason, as
// the account switch in OrgSwitcher.
//
// True only once THIS document has actually established a session: signOut also
// fires at boot for a browser with no tokens or stale ones, and reloading there
// would leave the login page reloading itself. Read from a signIn watcher and
// not from $authState, which the same event updates.
let sessionActive = false
signIn.watch(() => {
  sessionActive = true
})

// How long the reload waits for the revoke request. Unloading the document
// aborts it, and a refresh token that was never revoked stays valid for its
// full TTL — but a hung request must not strand the user in front of the
// previous tenant's data either.
const REVOKE_GRACE_MS = 2_000

const leaveSession = async (): Promise<void> => {
  const revoked = revokeSession()
  if (!sessionActive) {
    return
  }
  sessionActive = false
  // Read before the in-app guard redirects to /login, so signing back in
  // returns the user where they were.
  const from = window.location.pathname
  await Promise.race([
    revoked,
    new Promise((resolve) => setTimeout(resolve, REVOKE_GRACE_MS)),
  ])
  window.location.assign(
    from === '/' || from.startsWith('/login')
      ? '/login'
      : `/login?redirect=${encodeURIComponent(from)}`,
  )
}

signOut.watch(() => {
  void leaveSession()
})

// ============= Samples =============

// Init always yields a logged-in user; sign-in only when it is not a challenge.
sample({ clock: initAuthFx.doneData, target: signIn })

sample({
  clock: signInAuthFx.doneData,
  filter: (result: SignInResult): result is { user: UserEntity } =>
    'user' in result,
  fn: (result) => result as { user: UserEntity },
  target: signIn,
})

sample({ clock: selectAccountFx.doneData, target: signIn })

// Signup issues tokens immediately — flip the auth state too, or the form
// silently stays on the login page with a session already established.
sample({
  clock: signUpAuthFx.doneData,
  filter: (result: SignUpResult): result is SignInSuccess => 'user' in result,
  fn: (result) => result as SignInSuccess,
  target: signIn,
})

sample({
  clock: completeOidcFx.doneData,
  filter: (result: SignInResult): result is { user: UserEntity } =>
    'user' in result,
  fn: (result) => result as { user: UserEntity },
  target: signIn,
})

sample({
  clock: linkExistingAccountFx.doneData,
  filter: (result: SignInResult): result is { user: UserEntity } =>
    'user' in result,
  fn: (result) => result as { user: UserEntity },
  target: signIn,
})

sample({
  clock: passkeyLoginFx.doneData,
  filter: (result: SignInResult): result is { user: UserEntity } =>
    'user' in result,
  fn: (result) => result as { user: UserEntity },
  target: signIn,
})

sample({
  clock: completeTwoFactorFx.doneData,
  filter: (result: SignInResult): result is { user: UserEntity } =>
    'user' in result,
  fn: (result) => result as { user: UserEntity },
  target: signIn,
})

// Page-load authentication ends the session only when the server refused it.
// A transient failure keeps the tokens, shows that the server cannot be
// reached and tries again with a growing pause: signing out there would revoke
// a valid session and, through the storage event, sign out every other tab.
export const retryAuthInit = createEvent()
const bootRetryScheduled = createEvent()

export const $bootUnreachable = createStore(false)
  .on(bootRetryScheduled, () => true)
  .reset(signIn, signOut)

let bootRetryTimer: ReturnType<typeof setTimeout> | null = null
let bootRetryAttempt = 0

const cancelBootRetry = () => {
  if (bootRetryTimer !== null) clearTimeout(bootRetryTimer)
  bootRetryTimer = null
}

signIn.watch(() => {
  cancelBootRetry()
  bootRetryAttempt = 0
})
signOut.watch(cancelBootRetry)

bootRetryScheduled.watch(() => {
  cancelBootRetry()
  bootRetryTimer = setTimeout(() => {
    bootRetryTimer = null
    void initAuthFx()
  }, bootRetryDelay(bootRetryAttempt++))
})

retryAuthInit.watch(() => {
  if (initAuthFx.pending.getState()) return
  cancelBootRetry()
  void initAuthFx()
})

sample({
  clock: initAuthFx.failData,
  filter: (error: AxiosError | Error) => bootFailureAction(error) === 'retry',
  target: bootRetryScheduled,
})

sample({
  clock: initAuthFx.failData,
  filter: (error: AxiosError | Error) =>
    bootFailureAction(error) === 'sign-out',
  target: signOut,
})
