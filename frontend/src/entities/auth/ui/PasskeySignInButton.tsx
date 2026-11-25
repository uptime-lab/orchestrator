import { t } from 'i18next'
import { useState } from 'react'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { apiAuth } from '../api'
import { passkeyLoginFx } from '../model.ts'

// PasskeySignInButton starts a usernameless WebAuthn sign-in.
//
// No email field precedes it: the authenticator resolves which account to
// offer, which is what makes a passkey a complete sign-in rather than a step
// after a password. Hidden entirely on browsers without WebAuthn, so it never
// offers something that could only fail.
type PasskeySignInButtonProps = {
  // label overrides the button text; the second-factor step says "continue"
  // rather than "sign in", because the password has already been accepted.
  label?: string
  kind?: 'primary' | 'secondary'
  // autoFocus puts the keyboard on this button when it is the step's primary
  // action; the second-factor step has no input to land on then.
  autoFocus?: boolean
  // disabled lets the owning step hold this button while its OTHER action is
  // in flight, so a code submission and a passkey ceremony cannot both run.
  disabled?: boolean
}

export const PasskeySignInButton = ({
  label,
  kind = 'secondary',
  autoFocus = false,
  disabled = false,
}: PasskeySignInButtonProps = {}) => {
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  if (!apiAuth.isPasskeySupported()) return null

  const handleClick = async () => {
    setError(null)
    setPending(true)
    try {
      await passkeyLoginFx()
    } catch (e: unknown) {
      // A user who dismisses the browser's own prompt has not hit an error;
      // shouting at them about it would be wrong.
      if (e instanceof DOMException && e.name === 'NotAllowedError') {
        setError(null)
      } else {
        setError(extractApiErrorMessage(e, t('entities.auth.passkey.error')))
      }
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-2">
      <Button
        size="lg"
        kind={kind}
        autoFocus={autoFocus}
        disabled={pending || disabled}
        onClick={handleClick}
      >
        {label ?? t('entities.auth.passkey.signIn')}
      </Button>
      {error && <div className="text-sm text-red-9">{error}</div>}
    </div>
  )
}
