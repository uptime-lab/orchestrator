import { t } from 'i18next'

import { extractApiErrorMessage } from '@/shared/utils/apiError'

type SignInErrorLike = {
  response?: {
    status?: number
    headers?: Record<string, unknown>
    data?: { code?: unknown }
  }
}

// The two temporary sign-in refusals carry a code; both mean "wait", never
// "your password is wrong", so they get their own words. Everything else keeps
// the server's message.
export const signInErrorMessage = (
  error: unknown,
  fallback: string,
): string => {
  const response = (error as SignInErrorLike | undefined)?.response
  if (response?.status === 429) {
    const code = response.data?.code
    if (code === 'login_throttled') {
      const seconds = retryAfterSeconds(response.headers)
      return seconds
        ? t('entities.auth.signInError.throttled', { count: seconds })
        : t('entities.auth.signInError.throttledNoWait')
    }
    if (code === 'two_factor_locked') {
      return t('entities.auth.signInError.twoFactorLocked')
    }
  }
  return extractApiErrorMessage(error, fallback)
}

// Retry-After in its delta-seconds form, the only one the server sends. Axios
// lower-cases header names.
const retryAfterSeconds = (
  headers: Record<string, unknown> | undefined,
): number | null => {
  const raw = headers?.['retry-after']
  const value =
    typeof raw === 'number' ? raw : typeof raw === 'string' ? Number(raw) : NaN
  return Number.isFinite(value) && value > 0 ? Math.ceil(value) : null
}
