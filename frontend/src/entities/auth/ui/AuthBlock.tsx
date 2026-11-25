import { Link } from '@tanstack/react-router'
import { t } from 'i18next'
import { FC, useEffect, useState } from 'react'

import { useSignInOptions } from '@/entities/signInOptions'

import {
  useAuthState,
  useLoginChallenge,
  useTwoFactorChallenge,
} from '../hooks.ts'
import { signUpLinkShown, signUpOutlook } from '../installCopy.ts'
import { consumeSessionExpiredNotice } from '../model.ts'
import { AuthStatus, type AuthStateOut } from '../types.ts'
import { AccountSelectForm } from './AccountSelectForm.tsx'
import { AuthBlockContainer } from './AuthBlockContainer.tsx'
import { OidcAuthPanel } from './OidcAuthPanel.tsx'
import { PasskeySignInButton } from './PasskeySignInButton.tsx'
import { SignInForm } from './SignInForm.tsx'
import { SignUpForm } from './SignUpForm.tsx'
import { TwoFactorForm } from './TwoFactorForm.tsx'
import { useOidcConfig } from './useOidcConfig.ts'

type AuthBlockProps = {
  type: 'sign-in' | 'sign-up'
}

// SignInPanel makes the identity provider the primary path when OIDC is
// configured, with the password form tucked under a toggle. If OIDC is off,
// the password form is shown directly.
const SignInPanel: FC<{ authState: AuthStateOut }> = ({ authState }) => {
  const { loading, config } = useOidcConfig()

  // OIDC off, or still resolving: password is the only path. (loading
  // referenced so the hook value is used and the intent is explicit.)
  void loading
  if (!config) {
    return (
      <div className="flex flex-col gap-4">
        <PasskeySignInButton />
        <SignInForm authState={authState} />
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <PasskeySignInButton />
      <OidcAuthPanel
        mode="sign-in"
        config={config}
        emailForm={<SignInForm authState={authState} />}
      />
    </div>
  )
}

// SignUpPanel offers the SAME provider + invite path on the register page — a
// new account is created through the provider where the install allows it.
// Without it the register page showed only the email/password form, so the
// provider sign-up path was undiscoverable.
//
// What the page offers follows the install's registration state: its first
// account needs no code, a closed install says so instead of showing a form
// the backend refuses, and an unknown state keeps the page as it was.
const SignUpPanel: FC = () => {
  const { config } = useOidcConfig()
  const { options } = useSignInOptions()
  const outlook = signUpOutlook(options, config)

  const form = outlook.passwordForm ? (
    <SignUpForm inviteField={outlook.inviteField} />
  ) : null
  const note = outlook.note && (
    <p className="text-sm text-neutral-6">{outlook.note}</p>
  )

  if (!config || !outlook.oidc) {
    return (
      <div className="flex flex-col gap-4">
        {note}
        {form}
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-4">
      {note}
      <OidcAuthPanel mode="sign-up" config={config} emailForm={form} />
    </div>
  )
}

export const AuthBlock: FC<AuthBlockProps> = ({ type }) => {
  const authState = useAuthState()
  const { config: oidcConfig } = useOidcConfig()
  const { options: signInOptions } = useSignInOptions()
  const challenge = useLoginChallenge()
  const twoFactorChallenge = useTwoFactorChallenge()

  // Read-once on mount: a forced sign-out (dead refresh token) drops the user
  // here with no explanation, which reads as "the app signed me out for no
  // reason". The flag is consumed so a later deliberate visit
  // shows a plain sign-in card.
  const [sessionExpired] = useState(() => consumeSessionExpiredNotice())

  // we want to have last non-signed-in status to keep showing the form until everything is loaded
  const [initialAuthState, setInitialAuthState] = useState(authState)
  useEffect(() => {
    if (authState.status !== AuthStatus.LoggedIn) setInitialAuthState(authState)
  }, [authState])

  // A pending second factor takes over the card FIRST: it comes before the
  // account choice, and until it is satisfied there is no session at all.
  if (twoFactorChallenge && initialAuthState.status !== AuthStatus.LoggedIn) {
    return (
      <AuthBlockContainer>
        <TwoFactorForm
          twoFactorToken={twoFactorChallenge.token}
          methods={twoFactorChallenge.methods}
        />
      </AuthBlockContainer>
    )
  }

  // A pending multi-account challenge takes over the login card.
  if (challenge && initialAuthState.status !== AuthStatus.LoggedIn) {
    return (
      <AuthBlockContainer>
        <AccountSelectForm challenge={challenge} />
      </AuthBlockContainer>
    )
  }

  if (initialAuthState.status === AuthStatus.LoggedOut) {
    return type === 'sign-in' ? (
      <AuthBlockContainer
        title={t('entities.auth.signIn.title')}
        footer={
          // Hidden only where the install is known to admit no sign-up.
          signUpLinkShown(signInOptions, oidcConfig) ? (
            <>
              {t('entities.auth.signIn.footerTitle')}{' '}
              <Link to="/register" className="text-lime-6">
                {t('entities.auth.signIn.footerButton')}
              </Link>
            </>
          ) : undefined
        }
      >
        {sessionExpired && (
          <div className="mb-4 rounded-md bg-amber-6/10 border border-amber-6/50 px-3 py-2 text-sm text-amber-4">
            {t('entities.auth.sessionExpired')}
          </div>
        )}
        <SignInPanel authState={initialAuthState} />
      </AuthBlockContainer>
    ) : (
      <AuthBlockContainer
        title={t('entities.auth.signUp.title')}
        footer={
          <>
            {t('entities.auth.signUp.footerTitle')}{' '}
            <Link to="/login" className="text-lime-6">
              {t('entities.auth.signUp.footerButton')}
            </Link>
          </>
        }
      >
        <SignUpPanel />
      </AuthBlockContainer>
    )
  }
}
