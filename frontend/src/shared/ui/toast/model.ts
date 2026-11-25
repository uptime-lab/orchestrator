import { createEvent, createStore, sample } from 'effector'

import { isReauthCancelled } from '@/shared/utils/apiError'
import { commandRefusalText } from '@/shared/utils/commandRefusal'

/**
 * Toasts — the primitive this app was missing.
 *
 * Until now there was no way to tell the user anything, so ~20 call sites
 * swallowed failures into console.error and the codebase said so out loud
 * ("the app has no toast system, so warnings need a modal surface"). The
 * consequences were not cosmetic: nine device-mutating handlers in the node
 * panel reported nothing at all, so a refused reboot, shutdown or delete looked
 * exactly like a successful one, and every drag-to-assign on the OU rail failed
 * in silence — including OU deletion, whose own code comment notes the server
 * commonly refuses it.
 *
 * Deliberately minimal: a queue, four tones, auto-dismiss for the ones that are
 * only information, and no auto-dismiss for errors — an operator who looked away
 * during a fleet action must still find out it failed.
 */

export type ToastTone = 'success' | 'error' | 'warning' | 'info'

export type Toast = {
  id: string
  tone: ToastTone
  message: string
  /** Optional second line: the server's reason, a node name, a count. */
  detail?: string
}

/** How long a non-error toast stays up. Errors never auto-dismiss. */
export const TOAST_TTL_MS = 6000

/**
 * Bound on the visible queue. A batch action over a hundred nodes must not
 * paper the screen; the oldest go first because the newest is what the user
 * just caused.
 */
const MAX_VISIBLE = 4

let seq = 0
const nextId = () => `toast-${++seq}`

export const toastShown = createEvent<Omit<Toast, 'id'>>()
export const toastDismissed = createEvent<string>()
export const toastsCleared = createEvent()

export const $toasts = createStore<Toast[]>([])
  .on(toastShown, (list, toast) => {
    // Collapse an identical message that is already on screen instead of
    // stacking it. The query client retries a failed request three times by
    // default and refetches on window focus, so a single refused call would
    // otherwise raise four identical non-dismissing toasts and — with the queue
    // capped — evict everything else the operator had not read yet.
    const duplicate = list.find(
      (t) =>
        t.tone === toast.tone &&
        t.message === toast.message &&
        t.detail === toast.detail,
    )
    if (duplicate) return list

    const next = [...list, { ...toast, id: nextId() }]
    return next.length > MAX_VISIBLE
      ? next.slice(next.length - MAX_VISIBLE)
      : next
  })
  .on(toastDismissed, (list, id) => {
    // Return the SAME array when nothing matched, so a timer firing for a toast
    // the user already dismissed does not re-render every subscriber.
    if (!list.some((t) => t.id === id)) return list
    return list.filter((t) => t.id !== id)
  })
  .reset(toastsCleared)

/**
 * Convenience helpers. `showError` takes an unknown because every call site has
 * a caught error of unknown shape, and forcing each one to unwrap it is how
 * "just log it" won last time.
 */
export const showSuccess = (message: string, detail?: string) =>
  toastShown({ tone: 'success', message, detail })

export const showInfo = (message: string, detail?: string) =>
  toastShown({ tone: 'info', message, detail })

export const showWarning = (message: string, detail?: string) =>
  toastShown({ tone: 'warning', message, detail })

export const showError = (message: string, error?: unknown) => {
  // A dismissed identity prompt is the user's own choice, not a failure.
  if (isReauthCancelled(error)) return
  toastShown({ tone: 'error', message, detail: extractErrorText(error) })
}

/**
 * Pulls the most useful sentence out of whatever was thrown. The API answers
 * with { message } or { error }; axios wraps it; a thrown string is possible.
 * Falling back to err.message would surface "Request failed with status code
 * 403", which tells the user nothing they can act on, so that is used last.
 */
export const extractErrorText = (error?: unknown): string | undefined => {
  if (error == null) return undefined
  if (isReauthCancelled(error)) return undefined
  if (typeof error === 'string') return error

  const refusal = commandRefusalText(error)
  if (refusal) return refusal

  const maybe = error as {
    response?: { data?: { message?: string; error?: string } }
    message?: string
  }
  return (
    maybe.response?.data?.message ??
    maybe.response?.data?.error ??
    maybe.message ??
    undefined
  )
}

/** Auto-dismiss everything except errors. */
sample({
  clock: toastShown,
  source: $toasts,
  fn: (list) => list[list.length - 1],
}).watch((toast) => {
  if (!toast || toast.tone === 'error') return
  setTimeout(() => toastDismissed(toast.id), TOAST_TTL_MS)
})
