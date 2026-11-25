import { createFileRoute } from '@tanstack/react-router'
import { t } from 'i18next'
import { useEffect, useState } from 'react'

import { authHooks } from '@/entities/auth'
import { apiAuth } from '@/entities/auth/api'
import type { InvitePreview } from '@/entities/auth/api/acceptInvite'
import { AuthStatus } from '@/entities/auth/types'
import { AuthBlockContainer } from '@/entities/auth/ui/AuthBlockContainer.tsx'
import { InviteAcceptForm } from '@/entities/auth/ui/InviteAcceptForm'
import { InviteAcceptPanel } from '@/entities/auth/ui/InviteAcceptPanel'

import { extractApiErrorMessage } from '@/shared/utils/apiError'

type InviteSearch = {
  token?: string
}

// Invite handles every way someone can arrive at an invitation link.
//
// It sits at the ROOT, outside both layouts: `_authLayout` bounces anyone
// already signed in to `/`, which silently swallowed the page in the most
// common case of all — an existing user invited into a second account — and
// `_mainLayout` would demand a session a brand-new teammate does not have.
//
// The branch is driven by the INVITE, not by the session alone. Deciding from
// the session only produced the worst screen in the flow: an existing user was
// shown "set a name and password to create your account", which the server
// rejects outright because the address is already taken. The page has to know
// whether the invited address already exists before it can ask for anything.
const Invite = () => {
  const { token } = Route.useSearch()
  const auth = authHooks.useAuthState()
  const [preview, setPreview] = useState<InvitePreview | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!token) return
    let cancelled = false
    apiAuth
      .getInvitePreview(token)
      .then((p) => {
        if (!cancelled) setPreview(p)
      })
      .catch((e: unknown) => {
        if (!cancelled) {
          setError(extractApiErrorMessage(e, t('entities.auth.invite.error')))
        }
      })
    return () => {
      cancelled = true
    }
  }, [token])

  const body = () => {
    if (!token) {
      return <Message text={t('entities.auth.invite.missingToken')} />
    }
    if (error) {
      return <Message text={error} />
    }
    // Wait for the preview rather than guessing: a flash of the wrong form is
    // how someone ends up typing a password into a screen that cannot work.
    if (!preview) {
      return <Message text={t('entities.auth.invite.checking')} />
    }

    const signedInAs =
      auth.status === AuthStatus.LoggedIn ? auth.user?.email : undefined

    // Signed in as somebody else. Say exactly who, because the fix — sign out,
    // sign back in as the invited address — is otherwise pure guesswork.
    if (
      signedInAs &&
      signedInAs.toLowerCase() !== preview.email.toLowerCase()
    ) {
      return (
        <AuthBlockContainer title={t('entities.auth.invite.wrongUserTitle')}>
          <div className="flex flex-col gap-4">
            <div className="text-sm text-neutral-7">
              {t('entities.auth.invite.wrongUser', {
                invited: preview.email,
                current: signedInAs,
              })}
            </div>
            <a href="/login" className="text-sm text-lime-6">
              {t('entities.auth.invite.switchAccount')}
            </a>
          </div>
        </AuthBlockContainer>
      )
    }

    if (signedInAs) {
      return <InviteAcceptPanel token={token} preview={preview} />
    }

    // Not signed in, and the address already has an identity: signing up again
    // is impossible, so offer the only thing that works.
    if (preview.user_exists) {
      return (
        <AuthBlockContainer title={t('entities.auth.invite.signInTitle')}>
          <div className="flex flex-col gap-4">
            <div className="text-sm text-neutral-7">
              {t('entities.auth.invite.signInToAccept', {
                email: preview.email,
                account: preview.account_name,
              })}
            </div>
            <a
              href={`/login?redirect=${encodeURIComponent(`/invite?token=${token}`)}`}
              className="text-sm text-lime-6"
            >
              {t('entities.auth.invite.signIn')}
            </a>
          </div>
        </AuthBlockContainer>
      )
    }

    return <InviteAcceptForm token={token} preview={preview} />
  }

  return (
    <div className="flex flex-1">
      <div className="flex flex-[2] flex-col items-center justify-center gap-3 overflow-hidden">
        {body()}
      </div>
    </div>
  )
}

const Message = ({ text }: { text: string }) => (
  <div className="text-sm text-neutral-7">{text}</div>
)

export const Route = createFileRoute('/invite')({
  component: Invite,
  validateSearch: (search: Record<string, unknown>): InviteSearch =>
    search.token ? { token: String(search.token) } : {},
})
