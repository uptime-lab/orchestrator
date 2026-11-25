import { createFileRoute } from '@tanstack/react-router'
import { t } from 'i18next'
import { useEffect, useState } from 'react'

import { authModel } from '@/entities/auth'
import { apiAuth } from '@/entities/auth/api'
import { AuthBlockContainer } from '@/entities/auth/ui/AuthBlockContainer.tsx'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

type VerifySearch = { token?: string; pending?: boolean }

// PendingConfirmation is the screen a signed-in owner of an unconfirmed open
// sign-up is sent to: the API allows them nothing else until the address is
// confirmed, so the page says so and offers a new link.
const PendingConfirmation = () => {
  const [sending, setSending] = useState(false)
  const [status, setStatus] = useState<{
    kind: 'ok' | 'error'
    text: string
  } | null>(null)

  const resend = async () => {
    setSending(true)
    setStatus(null)
    try {
      await apiAuth.postResendVerification()
      setStatus({
        kind: 'ok',
        text: t('entities.auth.verifyEmail.pendingSent'),
      })
    } catch (err: unknown) {
      setStatus({
        kind: 'error',
        text: extractApiErrorMessage(
          err,
          t('entities.auth.verifyEmail.pendingSendError'),
        ),
      })
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="text-sm text-neutral-6">
        {t('entities.auth.verifyEmail.pendingBody')}
      </div>
      <Button size="md" kind="secondary" onClick={resend} disabled={sending}>
        {t('entities.auth.verifyEmail.pendingResend')}
      </Button>
      {status && (
        <div
          className={
            'text-xs ' + (status.kind === 'ok' ? 'text-lime-6' : 'text-red-9')
          }
        >
          {status.text}
        </div>
      )}
      <a href="/" className="text-sm text-lime-6">
        {t('entities.auth.verifyEmail.pendingContinue')}
      </a>
      <button
        type="button"
        className="text-left text-sm text-neutral-7"
        onClick={() => {
          authModel.events.signOut()
          window.location.assign('/login')
        }}
      >
        {t('entities.auth.verifyEmail.pendingSignOut')}
      </button>
    </div>
  )
}

type State = 'working' | 'done' | 'failed'

// VerifyEmail consumes the one-time token from the email link.
//
// It sits at the ROOT, deliberately outside both layouts. `_authLayout`
// redirects anyone already signed in straight to `/`, which silently swallowed
// this page in the most common case of all — clicking the link in the same
// browser you are logged into. `_mainLayout` would be the opposite mistake and
// demand a session the link does not need. Verification is valid in either
// state (the token IS the credential), so the page owns its own centering and
// renders for everyone.
const VerifyEmail = () => {
  const { token, pending } = Route.useSearch()
  const [state, setState] = useState<State>(token ? 'working' : 'failed')
  const [message, setMessage] = useState<string | null>(
    token ? null : t('entities.auth.verifyEmail.missingToken'),
  )

  useEffect(() => {
    if (!token) return
    let cancelled = false
    apiAuth
      .postVerifyEmail(token)
      .then(() => {
        if (!cancelled) setState('done')
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setMessage(
          extractApiErrorMessage(err, t('entities.auth.verifyEmail.failed')),
        )
        setState('failed')
      })
    return () => {
      cancelled = true
    }
  }, [token])

  if (!token && pending) {
    return (
      <div className="flex flex-1">
        <div className="flex flex-[2] flex-col items-center justify-center gap-3 overflow-hidden">
          <AuthBlockContainer
            title={t('entities.auth.verifyEmail.pendingTitle')}
          >
            <PendingConfirmation />
          </AuthBlockContainer>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-1">
      <div className="flex flex-[2] flex-col items-center justify-center gap-3 overflow-hidden">
        <AuthBlockContainer title={t('entities.auth.verifyEmail.title')}>
          {state === 'working' && (
            <div className="text-sm text-neutral-7">
              {t('entities.auth.verifyEmail.working')}
            </div>
          )}
          {state === 'done' && (
            <div className="flex flex-col gap-4">
              <div className="text-sm text-lime-6">
                {t('entities.auth.verifyEmail.done')}
              </div>
              <a href="/" className="text-sm text-lime-6">
                {t('entities.auth.verifyEmail.continue')}
              </a>
            </div>
          )}
          {state === 'failed' && (
            <div className="flex flex-col gap-4">
              <div className="text-sm text-red-9">{message}</div>
              <div className="text-xs text-neutral-7">
                {t('entities.auth.verifyEmail.resendHint')}
              </div>
              <a href="/login" className="text-sm text-lime-6">
                {t('entities.auth.oidc.backToLogin')}
              </a>
            </div>
          )}
        </AuthBlockContainer>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/verify-email')({
  component: VerifyEmail,
  validateSearch: (search: Record<string, unknown>): VerifySearch => ({
    token: search.token ? String(search.token) : undefined,
    pending:
      search.pending === true ||
      search.pending === 1 ||
      search.pending === '1' ||
      undefined,
  }),
})
