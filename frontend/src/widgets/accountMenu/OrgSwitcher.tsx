import { useUnit } from 'effector-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  $accounts,
  currentAccountId,
  fetchAccountsFx,
} from '@/entities/account/model'
import { apiAuth } from '@/entities/auth/api'

import { authToken, pinSessionIdentity } from '@/shared/utils/apiConfig'

// OrgSwitcher lists the accounts the current user belongs to and switches
// between them. Switching swaps the token pair and hard-reloads the app so
// every account-scoped store, cache and the push WebSocket re-initialize for
// the new account (a switch without a reload is not implemented).
export const OrgSwitcher = () => {
  const { t } = useTranslation()
  const accounts = useUnit($accounts)
  const [switching, setSwitching] = useState(false)
  const activeId = currentAccountId()

  useEffect(() => {
    fetchAccountsFx().catch(() => {})
  }, [])

  // Nothing to switch between.
  if (accounts.length < 2) {
    return null
  }

  const handleSwitch = async (accountId: string) => {
    if (accountId === activeId || switching) return
    setSwitching(true)
    try {
      const tokens = await apiAuth.postSwitchAccount(accountId)
      authToken.setTokens(tokens.access_token, tokens.refresh_token)
      // This tab made the switch and is about to reload into it; only the
      // OTHER tabs must treat the new account as someone else's session.
      pinSessionIdentity()
      window.location.assign('/')
    } catch {
      setSwitching(false)
    }
  }

  return (
    <div className="border-t border-neutral-11 py-2">
      <div className="px-4 pb-1 text-xxs uppercase tracking-widest text-neutral-7">
        {t('widgets.accountMenu.organizations')}
      </div>
      {accounts.map((account) => (
        <button
          key={account.id}
          type="button"
          disabled={switching}
          onClick={() => handleSwitch(account.id)}
          className={
            'w-full min-h-8 px-4 flex items-center text-sm text-left ' +
            (account.id === activeId
              ? 'text-lime-6 bg-neutral-11'
              : 'text-neutral-6 hover:bg-neutral-11')
          }
        >
          {account.name}
        </button>
      ))}
    </div>
  )
}
