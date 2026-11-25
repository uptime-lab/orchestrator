import { isDefinitiveRefreshRefusal } from '@/shared/utils/refreshPolicy'

// What a failed page-load authentication means for the stored session.
//
// The same split as a refresh: only an answer that refuses the session ends it.
// A rate limit, a server error or a request that never got an answer says
// nothing about the tokens, and ending the session for it would sign out every
// tab of this browser and revoke a session that is still valid.
export type BootFailureAction = 'sign-out' | 'retry' | 'ignore'

export const NO_TOKENS_MESSAGE = 'No tokens found'

const TRANSIENT_STATUSES = new Set([408, 429])

const isTransientFailure = (error: unknown): boolean => {
  const e = error as {
    isAxiosError?: unknown
    response?: { status?: unknown }
  } | null
  if (!e || e.isAxiosError !== true) return false
  const status = e.response?.status
  // No response at all: the request was lost, timed out or never left.
  if (typeof status !== 'number') return true
  return TRANSIENT_STATUSES.has(status) || status >= 500
}

export const bootFailureAction = (error: unknown): BootFailureAction => {
  if (error instanceof Error && error.message === NO_TOKENS_MESSAGE) {
    return 'sign-out'
  }
  if ((error as { code?: unknown } | null)?.code === 'ERR_CANCELED') {
    return 'ignore'
  }
  if (isDefinitiveRefreshRefusal(error)) return 'sign-out'
  if (isTransientFailure(error)) return 'retry'
  // Any other answer (the user is gone, an unexpected shape) is not something
  // waiting will fix.
  return 'sign-out'
}

// Pauses before each further attempt, the last one repeating. Unbounded on
// purpose: the tab has nothing else to show until the server answers.
export const BOOT_RETRY_DELAYS_MS: readonly number[] = [
  2_000, 5_000, 10_000, 30_000,
]

export const bootRetryDelay = (attempt: number): number =>
  BOOT_RETRY_DELAYS_MS[Math.min(attempt, BOOT_RETRY_DELAYS_MS.length - 1)]
