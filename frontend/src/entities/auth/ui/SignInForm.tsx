import { Link } from '@tanstack/react-router'
import { t } from 'i18next'
import { useState, type SyntheticEvent } from 'react'

import { useSignInOptions } from '@/entities/signInOptions'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'

import { SignInPayload } from '../api/postSignIn.ts'
import { passwordResetAffordance } from '../installCopy.ts'
import { signInAuthFx } from '../model.ts'
import { signInErrorMessage } from '../signInError.ts'
import { AuthState, AuthStateOut } from '../types.ts'
import { AUTH_LOCAL_STORAGE_EMAIL_KEY } from '../utils.ts'

export type AuthComponentProps = {
  authState: AuthState
}

type SignInComponentProps = AuthComponentProps & { authState: AuthStateOut }

export const SignInForm = (_: SignInComponentProps) => {
  const [formError, setFormError] = useState<string | null>(null)
  const { options } = useSignInOptions()
  const reset = passwordResetAffordance(options)

  const handleFormSubmit = (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setFormError(null)

    const data = new FormData(event.target as HTMLFormElement)

    const payload: SignInPayload = {
      email: (data.get('email') || '') as string,
      password: (data.get('password') || '') as string,
    }
    signInAuthFx(payload).catch((error: unknown) => {
      setFormError(
        signInErrorMessage(error, t('entities.auth.signIn.form.errorFallback')),
      )
    })
  }

  return (
    <form className="flex flex-col gap-5" onSubmit={handleFormSubmit}>
      {/* The message was set and never rendered: formError was read only as
          `!!formError`, so a failed sign-in showed red borders and no reason —
          including the ones a user can act on ("registration is currently
          closed", "too many attempts"). Same banner the sibling SignUpForm
          already uses. */}
      {formError && (
        <div
          role="alert"
          className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm"
        >
          {formError}
        </div>
      )}
      <Input
        id="email"
        name="email"
        type="email"
        label={t('entities.auth.signIn.form.label.email')}
        placeholder={t('entities.auth.signIn.form.placeholder.email')}
        required
        error={!!formError}
        autoComplete="email"
        defaultValue={
          localStorage.getItem(AUTH_LOCAL_STORAGE_EMAIL_KEY) || undefined
        }
      />
      <Input
        id="password"
        name="password"
        type="password"
        label={t('entities.auth.signIn.form.label.password')}
        required
        error={!!formError}
        autoComplete="password"
      />
      <Button
        size="lg"
        variant={formError ? 'destructive' : 'normal'}
        type="submit"
      >
        {t('entities.auth.signIn.form.submit')}
      </Button>
      {reset.kind === 'link' ? (
        <Link to="/forgot-password" className="text-sm text-lime-6">
          {t('entities.auth.signIn.form.forgotPassword')}
        </Link>
      ) : (
        <p className="text-sm text-neutral-7">{reset.text}</p>
      )}
    </form>
  )
}
