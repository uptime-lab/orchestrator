import { useUnit } from 'effector-react'
import { t } from 'i18next'
import { useState, type SyntheticEvent } from 'react'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'

import type { TwoFactorMethod } from '../api/postSignIn.ts'
import { completeTwoFactorFx, passkeyLoginFx } from '../model.ts'
import { signInErrorMessage } from '../signInError.ts'
import { AuthBlockContainer } from './AuthBlockContainer.tsx'
import { PasskeySignInButton } from './PasskeySignInButton.tsx'

type TwoFactorFormProps = {
  twoFactorToken: string
  // methods says how the server will accept this challenge being completed.
  // It drives what is shown, never what is enforced: each method is checked
  // on its own server path regardless of what the form offered.
  methods: TwoFactorMethod[]
}

// TwoFactorForm is the second step of a password login.
//
// With a code method it accepts either a code from the authenticator or one
// of the single-use recovery codes — the server decides which, so the field
// stays one input and the user does not have to classify what they are
// holding. With a passkey method it offers the ordinary passkey sign-in,
// which is a complete login on its own: the password step has already been
// passed, so finishing with the passkey ends the challenge.
export const TwoFactorForm = ({
  twoFactorToken,
  methods,
}: TwoFactorFormProps) => {
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  // The passkey ceremony is a separate effect with its own pending state;
  // while the browser's prompt is open the code form must not also submit,
  // or two completions race for the same session.
  const passkeyPending = useUnit(passkeyLoginFx.pending)

  const acceptsCode = methods.includes('totp')
  const acceptsPasskey = methods.includes('passkey')

  const handleSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setError(null)
    setPending(true)
    const data = new FormData(event.target as HTMLFormElement)
    try {
      await completeTwoFactorFx({
        twoFactorToken,
        code: (data.get('code') || '') as string,
      })
    } catch (e: unknown) {
      setPending(false)
      setError(signInErrorMessage(e, t('entities.auth.twoFactor.error')))
    }
  }

  return (
    <AuthBlockContainer title={t('entities.auth.twoFactor.title')}>
      <div className="flex flex-col gap-5">
        {acceptsPasskey && (
          <>
            <p className="text-sm text-neutral-7">
              {t('entities.auth.twoFactor.passkeySubTitle')}
            </p>
            <PasskeySignInButton
              label={t('entities.auth.twoFactor.passkeyContinue')}
              kind={acceptsCode ? 'secondary' : 'primary'}
              autoFocus={!acceptsCode}
              disabled={pending}
            />
          </>
        )}
        {acceptsPasskey && acceptsCode && (
          <div className="text-center text-xs text-neutral-7">
            {t('entities.auth.twoFactor.orCode')}
          </div>
        )}
        {acceptsCode && (
          <form className="flex flex-col gap-5" onSubmit={handleSubmit}>
            <p className="text-sm text-neutral-7">
              {t('entities.auth.twoFactor.subTitle')}
            </p>
            <Input
              id="code"
              name="code"
              type="text"
              label={t('entities.auth.twoFactor.code')}
              autoComplete="one-time-code"
              inputMode="text"
              autoFocus
              required
              error={!!error}
            />
            <Button
              size="lg"
              type="submit"
              variant={error ? 'destructive' : 'normal'}
              disabled={pending || passkeyPending}
            >
              {t('entities.auth.twoFactor.submit')}
            </Button>
            {error && <div className="text-sm text-red-9">{error}</div>}
            <div className="text-xs text-neutral-7">
              {t('entities.auth.twoFactor.recoveryHint')}
            </div>
          </form>
        )}
      </div>
    </AuthBlockContainer>
  )
}
