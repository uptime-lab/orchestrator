import { t } from 'i18next'
import type { FC } from 'react'

type RouteErrorProps = {
  error?: unknown
  // Overrides for a screen that is not a crash, such as a server that cannot
  // be reached yet.
  title?: string
  body?: string
  // Replaces the reload button; the home button is dropped with it.
  retry?: { label: string; onClick: () => void; disabled?: boolean }
}

/**
 * RouteError is the app's error boundary, wired as the router's
 * defaultErrorComponent.
 *
 * Until this existed there was NO boundary anywhere — no errorComponent, no
 * defaultErrorComponent, no componentDidCatch — so one unexpected shape from an
 * older agent in the deeply nested node sidebar data rendered a blank white page
 * with no way back except typing a URL.
 *
 * Deliberately plain: it must not itself depend on the router, the query client,
 * the socket or the auth store, because any of those may be the thing that
 * failed. Reload is a full document reload for the same reason — a soft
 * navigation would keep the broken state in memory.
 *
 * It states that nothing was changed on the operator's devices. That is the first
 * question in a product that wipes disks and reboots hardware, and a blank page
 * answers it with silence.
 */
export const RouteError: FC<RouteErrorProps> = ({
  error,
  title,
  body,
  retry,
}) => {
  const message =
    error instanceof Error
      ? error.message
      : typeof error === 'string'
        ? error
        : undefined

  return (
    <div className="flex min-h-[60vh] w-full items-center justify-center p-6">
      <div className="w-full max-w-lg rounded-lg border border-red-900/40 bg-red-950/20 p-6">
        <h1 className="text-base font-semibold text-white">
          {title ?? t('shared.routeError.title')}
        </h1>
        <p className="mt-2 text-sm text-gray-300">
          {body ?? t('shared.routeError.body')}
        </p>

        <div className="mt-5 flex flex-wrap gap-3">
          {retry ? (
            <button
              type="button"
              onClick={retry.onClick}
              disabled={retry.disabled}
              className="rounded-md bg-white px-3 py-2 text-sm font-medium text-gray-900 hover:bg-gray-200 disabled:opacity-50"
            >
              {retry.label}
            </button>
          ) : (
            <>
              <button
                type="button"
                onClick={() => window.location.reload()}
                className="rounded-md bg-white px-3 py-2 text-sm font-medium text-gray-900 hover:bg-gray-200"
              >
                {t('shared.routeError.reload')}
              </button>
              <button
                type="button"
                onClick={() => window.location.assign('/')}
                className="rounded-md border border-gray-600 px-3 py-2 text-sm font-medium text-gray-200 hover:bg-gray-800"
              >
                {t('shared.routeError.home')}
              </button>
            </>
          )}
        </div>

        {message ? (
          <details className="mt-5">
            <summary className="cursor-pointer text-xs text-gray-400">
              {t('shared.routeError.details')}
            </summary>
            {/* The message only. A stack trace here would be read as noise and
                tells an operator nothing they can act on. */}
            <p className="mt-2 break-words font-mono text-xs text-gray-400">
              {message}
            </p>
          </details>
        ) : null}
      </div>
    </div>
  )
}
