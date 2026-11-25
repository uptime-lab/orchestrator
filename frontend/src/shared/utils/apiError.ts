import { commandRefusalText } from './commandRefusal'

// Shape of an axios-like error we care about for surfacing messages.
type ApiErrorLike = {
  response?: { data?: { message?: unknown; error?: unknown } }
  message?: unknown
}

// A request the server answered with "confirm your identity" whose prompt the
// user then dismissed. The interceptor marks the rejected error; it is not a
// failure to report, so every message helper turns it into nothing.
const REAUTH_CANCELLED = 'reauthCancelled'

export const markReauthCancelled = (error: unknown): void => {
  if (error && typeof error === 'object') {
    ;(error as Record<string, unknown>)[REAUTH_CANCELLED] = true
  }
}

export const isReauthCancelled = (error: unknown): boolean =>
  !!error &&
  typeof error === 'object' &&
  (error as Record<string, unknown>)[REAUTH_CANCELLED] === true

// Best-effort extraction of a human-readable message from an unknown error,
// falling back to the provided default. A cancelled identity prompt yields
// an empty string: callers render an error only when there is text.
export const extractApiErrorMessage = (
  error: unknown,
  fallback: string,
): string => {
  if (isReauthCancelled(error)) {
    return ''
  }
  const refusal = commandRefusalText(error)
  if (refusal) {
    return refusal
  }
  const err = error as ApiErrorLike
  const data = err?.response?.data
  if (data && typeof data.message === 'string') {
    return data.message
  }
  if (data && typeof data.error === 'string') {
    return data.error
  }
  if (typeof err?.message === 'string') {
    return err.message
  }
  return fallback
}
