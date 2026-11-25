import { t } from 'i18next'
import { FC, ReactNode, useState } from 'react'

import { Button } from '@/shared/ui/button/button.tsx'

import { OidcConfig, startOidcLogin } from '../api/oidc.ts'
import {
  oidcAsksForInvite,
  oidcButtonText,
  oidcProviderName,
} from '../installCopy.ts'

type OidcAuthPanelProps = {
  // 'sign-in': the provider is framed as logging in; 'sign-up': as creating
  // an account. The invite field is the same gate in both — a new account is
  // only provisioned when a valid code is supplied.
  mode: 'sign-in' | 'sign-up'
  // The install's provider (GET /auth/oidc/config): its name words the
  // button, its sign-up rule decides whether the invite field is shown.
  config: OidcConfig
  // The email/password form revealed under the "or with email" toggle; null
  // where the install admits no password sign-up.
  emailForm: ReactNode | null
}

// OidcAuthPanel makes the identity provider the primary path on BOTH the
// sign-in and sign-up pages: a big provider button + the invite field where
// the install asks for one, with the email/password form tucked under a
// toggle. On sign-up the copy makes explicit that the provider creates an
// account — otherwise that path is undiscoverable from the register page.
export const OidcAuthPanel: FC<OidcAuthPanelProps> = ({
  mode,
  config,
  emailForm,
}) => {
  const [showEmail, setShowEmail] = useState(false)
  const [invite, setInvite] = useState('')

  const isSignUp = mode === 'sign-up'
  const buttonLabel = oidcButtonText(mode, config)
  const askInvite = oidcAsksForInvite(config)
  const invitePlaceholder = isSignUp
    ? t('entities.auth.signUp.invitePlaceholder')
    : t('entities.auth.signIn.invitePlaceholder')
  const inviteHint = isSignUp
    ? t('entities.auth.signUp.inviteHintProvider', {
        provider: oidcProviderName(config),
      })
    : t('entities.auth.signIn.inviteHint')
  const orEmail = isSignUp
    ? t('entities.auth.signUp.orEmail')
    : t('entities.auth.signIn.orEmail')
  const usePassword = isSignUp
    ? t('entities.auth.signUp.usePassword')
    : t('entities.auth.signIn.usePassword')

  return (
    <div className="flex flex-col gap-4">
      <Button
        size="lg"
        variant="normal"
        grow
        onClick={() =>
          startOidcLogin(askInvite ? invite.trim() || undefined : undefined)
        }
      >
        {buttonLabel}
      </Button>
      {/* Where an invite code is configured, a new account through the
          provider requires it; an existing user can leave it blank. */}
      {askInvite && (
        <>
          <input
            type="text"
            value={invite}
            onChange={(e) => setInvite(e.target.value)}
            placeholder={invitePlaceholder}
            autoComplete="off"
            className="w-full bg-neutral-13 border border-neutral-11 rounded px-3 py-2 text-sm text-neutral-2 placeholder:text-neutral-8"
          />
          <p className="text-xs text-neutral-8 -mt-2">{inviteHint}</p>
        </>
      )}
      {emailForm === null ? null : showEmail ? (
        <>
          <div className="flex items-center gap-3 text-xs text-neutral-8">
            <span className="flex-1 border-t border-neutral-12" />
            {orEmail}
            <span className="flex-1 border-t border-neutral-12" />
          </div>
          {emailForm}
        </>
      ) : (
        <button
          type="button"
          onClick={() => setShowEmail(true)}
          className="text-xs text-neutral-7 hover:text-neutral-5 self-center"
        >
          {usePassword}
        </button>
      )}
    </div>
  )
}
