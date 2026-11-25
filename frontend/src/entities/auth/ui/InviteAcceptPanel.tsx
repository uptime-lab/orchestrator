import { t } from 'i18next'
import { useState } from 'react'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { apiAuth } from '../api'
import { AuthBlockContainer } from './AuthBlockContainer.tsx'

type InviteAcceptPanelProps = {
  token: string
  preview?: { email: string; account_name: string; role: string }
}

// InviteAcceptPanel is the SIGNED-IN half of an invite link: an existing
// Orchestrator identity being added to another account. It only confirms —
// there is no name or password to collect, the user already has both.
//
// Accepting adds a membership but does not switch the active account (the
// current session's token is pinned to the old one), so the success state
// points at the account switcher rather than pretending the context changed.
export const InviteAcceptPanel = ({
  token,
  preview,
}: InviteAcceptPanelProps) => {
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [joined, setJoined] = useState(false)

  const handleAccept = async () => {
    setError(null)
    setPending(true)
    try {
      await apiAuth.postAcceptInvite(token)
      setJoined(true)
    } catch (e: unknown) {
      setError(extractApiErrorMessage(e, t('entities.auth.invite.error')))
    } finally {
      setPending(false)
    }
  }

  if (joined) {
    return (
      <AuthBlockContainer title={t('entities.auth.invite.acceptedTitle')}>
        <div className="flex flex-col gap-4">
          <div className="text-sm text-lime-6">
            {t('entities.auth.invite.accepted')}
          </div>
          <div className="text-xs text-neutral-7">
            {t('entities.auth.invite.acceptedHint')}
          </div>
          <a href="/" className="text-sm text-lime-6">
            {t('entities.auth.verifyEmail.continue')}
          </a>
        </div>
      </AuthBlockContainer>
    )
  }

  return (
    <AuthBlockContainer title={t('entities.auth.invite.acceptTitle')}>
      <div className="flex flex-col gap-5">
        <p className="text-sm text-neutral-7">
          {preview
            ? t('entities.auth.invite.acceptNamed', {
                account: preview.account_name,
                role: preview.role,
              })
            : t('entities.auth.invite.acceptSubTitle')}
        </p>
        <Button
          size="lg"
          variant={error ? 'destructive' : 'normal'}
          disabled={pending}
          onClick={handleAccept}
        >
          {t('entities.auth.invite.acceptSubmit')}
        </Button>
        {error && <div className="text-sm text-red-9">{error}</div>}
      </div>
    </AuthBlockContainer>
  )
}
