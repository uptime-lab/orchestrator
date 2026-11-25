import { t } from 'i18next'
import { useRef, useState, type FC, type SyntheticEvent } from 'react'

import { RegisterPayload } from '@/entities/auth/api/postSignUp.ts'
import {
  getTermsUrl,
  TERMS_FIELD_NAME,
  termsAcceptance,
  TermsAgreement,
} from '@/entities/terms'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { isBlank } from '@/shared/utils/formRules'
import { passwordTooLong } from '@/shared/utils/passwordLimits'

import { signUpAndLoginAuthFx } from '../model.ts'

// Must match user.MinPasswordLength on the backend. It was 6 here while the
// server refused anything under 8 — and refused it with "password is required",
// so the form told people to supply the password they were looking at. Keep
// the two in step.
const MIN_PASSWORD_LENGTH = 8

type SignUpFormProps = {
  // False where the install admits the sign-up without a code (its first
  // account, or open registration), so the form does not ask for something
  // nobody needs.
  inviteField?: boolean
  // Start on the "check your email" answer (the state an open sign-up ends
  // in); for rendering that state directly.
  initiallyCheckEmail?: boolean
}

// SignUpCheckEmail is the answer to an open sign-up: no session exists until
// the address is confirmed, and the same words are shown whether or not the
// address already had an account.
export const SignUpCheckEmail: FC = () => (
  <div className="flex flex-col gap-3" data-testid="signup-check-email">
    <p className="text-sm text-lime-6">
      {t('entities.auth.signUp.checkEmail.title')}
    </p>
    <p className="text-sm text-neutral-6">
      {t('entities.auth.signUp.checkEmail.body')}
    </p>
    <a href="/login" className="text-sm text-lime-6">
      {t('entities.auth.signUp.checkEmail.signIn')}
    </a>
  </div>
)

export const SignUpForm: FC<SignUpFormProps> = ({
  inviteField = true,
  initiallyCheckEmail = false,
}) => {
  const formRef = useRef<HTMLFormElement>(null)
  const [checkEmail, setCheckEmail] = useState(initiallyCheckEmail)
  const [isProcessing, setIsProcessing] = useState<boolean>(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [isSubmittedOnce, setIsSubmittedOnce] = useState(false)

  const handleFormSubmit = (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setFormError(null)
    setIsProcessing(true)
    setIsSubmittedOnce(true)

    const data = new FormData(event.target as HTMLFormElement)

    const firstName = (data.get('name') || '') as string
    const lastName = (data.get('surname') || '') as string
    const fullName = `${firstName} ${lastName}`.trim()

    // The checkbox is `required`, so a browser will not submit it unticked;
    // this is the same rule for anything that skips native validation.
    const terms = termsAcceptance(
      getTermsUrl(),
      data.get(TERMS_FIELD_NAME) === 'on',
    )

    const inviteCode = ((data.get('invite-code') || '') as string).trim()
    const payload: RegisterPayload = {
      name: fullName,
      email: (data.get('email') || '') as string,
      password: (data.get('password') || '') as string,
      invite_code: inviteCode || undefined,
      terms_accepted: terms.terms_accepted,
    }

    if (isBlank(fullName)) {
      setFormError(t('shared.form.nameRequired'))
      setIsProcessing(false)
      return
    }

    if (passwordTooLong(payload.password)) {
      setFormError(t('shared.password.tooLong'))
      setIsProcessing(false)
      return
    }

    if (data.get('confirm-password') !== data.get('password')) {
      setFormError(t('entities.auth.signUp.form.error.passwordMismatch'))
      setIsProcessing(false)
      return
    }

    if (!terms.ok) {
      setFormError(t('entities.terms.required'))
      setIsProcessing(false)
      return
    }

    signUpAndLoginAuthFx(payload)
      .then((result) => {
        if ('checkEmail' in result) setCheckEmail(true)
      })
      .catch((e) => {
        // The API responds with {success, message}; surface the actual reason
        // (e.g. "invalid invite code") instead of a generic failure.
        const body = e.response?.data
        const message = typeof body === 'string' ? body : body?.message || null
        setFormError(
          message
            ? t('entities.auth.signUp.form.error.signUpFailedWithReason', {
                message,
              })
            : t('entities.auth.signUp.form.error.signUpFailed'),
        )
      })
      .finally(() => setIsProcessing(false))
  }

  const handleConfirmPasswordChange = () => {
    if (formRef.current && isSubmittedOnce) {
      const data = new FormData(formRef.current)
      const password = data.get('password')
      const confirm = data.get('confirm-password')
      if (password && confirm && confirm !== password) {
        setFormError(t('entities.auth.signUp.form.error.passwordMismatch'))
      } else {
        setFormError(null)
      }
    }
  }

  if (checkEmail) return <SignUpCheckEmail />

  return (
    <form
      className="flex flex-col gap-5"
      onSubmit={handleFormSubmit}
      ref={formRef}
    >
      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <div className="flex w-full gap-5">
        <Input
          id="name"
          name="name"
          type="text"
          label={t('entities.auth.signUp.form.label.firstName')}
          placeholder={t('entities.auth.signUp.form.placeholder.firstName')}
          required
          error={!!formError}
          autoComplete="given-name"
        />
        <Input
          id="surname"
          name="surname"
          type="text"
          label={t('entities.auth.signUp.form.label.lastName')}
          placeholder={t('entities.auth.signUp.form.placeholder.lastName')}
          required
          error={!!formError}
          autoComplete="family-name"
        />
      </div>

      <Input
        id="email"
        name="email"
        type="email"
        label={t('entities.auth.signUp.form.label.email')}
        placeholder={t('entities.auth.signUp.form.placeholder.email')}
        required
        error={!!formError}
        autoComplete="work email"
      />

      <Input
        id="password"
        name="password"
        type="password"
        label={t('entities.auth.signUp.form.label.password')}
        minLength={MIN_PASSWORD_LENGTH}
        required
        error={!!formError}
        autoComplete="new-password"
      />

      <p className="-mt-2 text-sm text-neutral-6">
        {t('entities.auth.signUp.form.hint.password', {
          count: MIN_PASSWORD_LENGTH,
        })}
      </p>

      <Input
        id="confirm-password"
        name="confirm-password"
        type="password"
        label={t('entities.auth.signUp.form.label.passwordConfirm')}
        minLength={MIN_PASSWORD_LENGTH}
        required
        error={!!formError}
        autoComplete="new-password"
        onChange={handleConfirmPasswordChange}
      />

      {/* Registration is invite-gated server-side once the install has an
          account, unless the operator opened it; the field is hidden only
          where the install says no code is needed. */}
      {inviteField && (
        <Input
          id="invite-code"
          name="invite-code"
          type="text"
          label={t('entities.auth.signUp.form.label.inviteCode')}
          placeholder={t('entities.auth.signUp.form.placeholder.inviteCode')}
          error={!!formError}
          autoComplete="off"
        />
      )}

      <TermsAgreement />

      <Button
        size="lg"
        variant={formError ? 'destructive' : 'normal'}
        type="submit"
        disabled={isProcessing}
      >
        {t('entities.auth.signUp.form.submit')}
      </Button>
    </form>
  )
}
