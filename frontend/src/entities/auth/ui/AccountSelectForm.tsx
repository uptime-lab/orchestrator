import { t } from 'i18next'
import { useState } from 'react'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { LoginChallenge } from '../api/postSignIn.ts'
import { selectAccountFx } from '../model.ts'

type AccountSelectFormProps = {
  challenge: LoginChallenge
}

// AccountSelectForm lets a multi-account user pick which organization to enter
// after a successful password login. Rendered inside the /login page from the
// pending challenge; the user stays LoggedOut until selection completes.
export const AccountSelectForm = ({ challenge }: AccountSelectFormProps) => {
  const [error, setError] = useState<string | null>(null)
  const [pendingId, setPendingId] = useState<string | null>(null)

  const handleSelect = (accountId: string) => {
    setError(null)
    setPendingId(accountId)
    selectAccountFx({
      pre_token: challenge.pre_token,
      account_id: accountId,
    }).catch((e: unknown) => {
      setPendingId(null)
      setError(
        extractApiErrorMessage(e, t('entities.auth.selectAccount.error')),
      )
    })
  }

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h2 className="text-base font-bold mb-1">
          {t('entities.auth.selectAccount.title')}
        </h2>
        <p className="text-sm text-neutral-7">
          {t('entities.auth.selectAccount.subTitle')}
        </p>
      </div>
      <div className="flex flex-col gap-2">
        {challenge.accounts.map((account) => (
          <Button
            key={account.id}
            size="lg"
            variant="normal"
            disabled={pendingId !== null}
            onClick={() => handleSelect(account.id)}
          >
            {account.name}
          </Button>
        ))}
      </div>
      {error && <div className="text-sm text-red-9">{error}</div>}
    </div>
  )
}
