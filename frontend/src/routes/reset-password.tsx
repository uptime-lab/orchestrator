import { createFileRoute } from '@tanstack/react-router'
import { t } from 'i18next'
import { useState, type SyntheticEvent } from 'react'

import { apiAuth } from '@/entities/auth/api'
import { AuthBlockContainer } from '@/entities/auth/ui/AuthBlockContainer.tsx'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { passwordTooLong } from '@/shared/utils/passwordLimits'

type ResetSearch = { token?: string }

// ResetPassword consumes the one-time token from the email link.
//
// Like /verify-email it sits at the ROOT, outside both layouts: `_authLayout`
// bounces anyone already signed in to `/`, which would swallow the page in a
// browser that still holds a session — and a stolen-session reset is exactly
// the case where the user MUST be able to finish. The token is the credential,
// so no session is required either.
const ResetPassword = () => {
  const { token } = Route.useSearch()
  const [error, setError] = useState<string | null>(
    token ? null : t('entities.auth.resetPassword.missingToken'),
  )
  const [pending, setPending] = useState(false)
  const [done, setDone] = useState(false)

  const handleSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    if (!token) return
    setError(null)
    const data = new FormData(event.target as HTMLFormElement)
    const password = (data.get('password') || '') as string
    // Checked here because a refused reset can spend the one-time link.
    if (passwordTooLong(password)) {
      setError(t('shared.password.tooLong'))
      return
    }
    setPending(true)
    try {
      await apiAuth.postResetPassword(token, password)
      setDone(true)
    } catch (e: unknown) {
      setError(
        extractApiErrorMessage(e, t('entities.auth.resetPassword.failed')),
      )
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="flex flex-1">
      <div className="flex flex-[2] flex-col items-center justify-center gap-3 overflow-hidden">
        <AuthBlockContainer title={t('entities.auth.resetPassword.title')}>
          {done ? (
            <div className="flex flex-col gap-4">
              <div className="text-sm text-lime-6">
                {t('entities.auth.resetPassword.done')}
              </div>
              <div className="text-xs text-neutral-7">
                {t('entities.auth.resetPassword.signedOutHint')}
              </div>
              <a href="/login" className="text-sm text-lime-6">
                {t('entities.auth.oidc.backToLogin')}
              </a>
            </div>
          ) : (
            <form className="flex flex-col gap-5" onSubmit={handleSubmit}>
              <p className="text-sm text-neutral-7">
                {t('entities.auth.resetPassword.subTitle')}
              </p>
              <Input
                id="password"
                name="password"
                type="password"
                label={t('entities.auth.resetPassword.password')}
                autoComplete="new-password"
                minLength={8}
                required
                disabled={!token}
                error={!!error}
              />
              <Button
                size="lg"
                type="submit"
                variant={error ? 'destructive' : 'normal'}
                disabled={pending || !token}
              >
                {t('entities.auth.resetPassword.submit')}
              </Button>
              {error && <div className="text-sm text-red-9">{error}</div>}
              <a href="/forgot-password" className="text-sm text-lime-6">
                {t('entities.auth.resetPassword.requestNew')}
              </a>
            </form>
          )}
        </AuthBlockContainer>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/reset-password')({
  component: ResetPassword,
  validateSearch: (search: Record<string, unknown>): ResetSearch => ({
    token: search.token ? String(search.token) : undefined,
  }),
})
