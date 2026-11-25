import { Link } from '@tanstack/react-router'
import { t } from 'i18next'
import { useState, type SyntheticEvent } from 'react'

import { useSignInOptions } from '@/entities/signInOptions'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { apiAuth } from '../api'
import { AuthBlockContainer } from './AuthBlockContainer.tsx'

// ForgotPasswordForm requests a reset link.
//
// On success it shows a deliberately vague confirmation. The server cannot tell
// the caller whether the address is registered without becoming a directory of
// users, so the UI must not imply it either — "if that address belongs to an
// account" is the honest wording, and it is also what the API returns.
export const ForgotPasswordForm = () => {
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [sent, setSent] = useState(false)
  const { options } = useSignInOptions()

  const handleSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setError(null)
    setPending(true)
    const data = new FormData(event.target as HTMLFormElement)
    try {
      await apiAuth.postForgotPassword((data.get('email') || '') as string)
      setSent(true)
    } catch (e: unknown) {
      setError(
        extractApiErrorMessage(e, t('entities.auth.forgotPassword.error')),
      )
    } finally {
      setPending(false)
    }
  }

  // Without mail the backend refuses the request; reached by a bookmark or
  // a typed URL, the page says who can help instead of offering a form.
  if (options?.email_delivery === false) {
    return (
      <AuthBlockContainer title={t('entities.auth.forgotPassword.title')}>
        <div className="flex flex-col gap-4">
          <p className="text-sm text-neutral-7">
            {t('entities.auth.forgotPassword.noMail')}
          </p>
          <Link to="/login" className="text-sm text-lime-6">
            {t('entities.auth.oidc.backToLogin')}
          </Link>
        </div>
      </AuthBlockContainer>
    )
  }

  if (sent) {
    return (
      <AuthBlockContainer title={t('entities.auth.forgotPassword.title')}>
        <div className="flex flex-col gap-4">
          <div className="text-sm text-lime-6">
            {t('entities.auth.forgotPassword.sent')}
          </div>
          <div className="text-xs text-neutral-7">
            {t('entities.auth.forgotPassword.sentHint')}
          </div>
          <Link to="/login" className="text-sm text-lime-6">
            {t('entities.auth.oidc.backToLogin')}
          </Link>
        </div>
      </AuthBlockContainer>
    )
  }

  return (
    <AuthBlockContainer title={t('entities.auth.forgotPassword.title')}>
      <form className="flex flex-col gap-5" onSubmit={handleSubmit}>
        <p className="text-sm text-neutral-7">
          {t('entities.auth.forgotPassword.subTitle')}
        </p>
        <Input
          id="email"
          name="email"
          type="email"
          label={t('entities.auth.signIn.form.label.email')}
          placeholder={t('entities.auth.signIn.form.placeholder.email')}
          autoComplete="email"
          required
          error={!!error}
        />
        <Button
          size="lg"
          type="submit"
          variant={error ? 'destructive' : 'normal'}
          disabled={pending}
        >
          {t('entities.auth.forgotPassword.submit')}
        </Button>
        {error && <div className="text-sm text-red-9">{error}</div>}
        <Link to="/login" className="text-sm text-lime-6">
          {t('entities.auth.oidc.backToLogin')}
        </Link>
      </form>
    </AuthBlockContainer>
  )
}
