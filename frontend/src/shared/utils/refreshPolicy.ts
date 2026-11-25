// Which refresh failures end the session, and how the others are retried.
//
// Only a definitive refusal means the refresh token is dead: the server
// answered and said no. A 429 from the shared auth rate limit, a 5xx or a
// request that never got an answer says nothing about the token, so signing
// the user out for it throws away a valid session and whatever was unsaved in
// the tab.

// Thrown when there is no refresh token to spend at all.
export class RefreshTokenMissingError extends Error {
  constructor() {
    super('No refresh token available')
    this.name = 'RefreshTokenMissingError'
  }
}

// The answers /auth/refresh gives for a token it will never accept: 400 for a
// malformed request, 401 for an invalid, expired, revoked or reused token and
// for a membership that is gone or disabled, 403 for a refusal by policy.
const DEFINITIVE_STATUSES = new Set([400, 401, 403])

export const isDefinitiveRefreshRefusal = (error: unknown): boolean => {
  if (error instanceof RefreshTokenMissingError) return true
  const status = (error as { response?: { status?: unknown } } | null)?.response
    ?.status
  return typeof status === 'number' && DEFINITIVE_STATUSES.has(status)
}

// Pauses between attempts of one refresh. Short and few: the caller is a user
// action or a socket reconnect, and either can try again later on its own.
export const REFRESH_RETRY_DELAYS_MS: readonly number[] = [1_000, 3_000]

const defaultSleep = (ms: number) =>
  new Promise<void>((resolve) => {
    setTimeout(resolve, ms)
  })

// retryTransient runs fn, retrying after each delay while the failure is not a
// definitive refusal. The last failure is rethrown as is.
export const retryTransient = async <T>(
  fn: () => Promise<T>,
  delaysMs: readonly number[] = REFRESH_RETRY_DELAYS_MS,
  sleep: (ms: number) => Promise<void> = defaultSleep,
): Promise<T> => {
  for (let attempt = 0; ; attempt++) {
    try {
      return await fn()
    } catch (error) {
      if (isDefinitiveRefreshRefusal(error) || attempt >= delaysMs.length) {
        throw error
      }
      await sleep(delaysMs[attempt])
    }
  }
}
