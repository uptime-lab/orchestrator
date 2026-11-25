import { createEffect, createStore } from 'effector'

import { apiAuth } from '@/entities/auth/api'
import { AccountOption } from '@/entities/auth/api/postSignIn'

import { authToken } from '@/shared/utils/apiConfig'

import { apiAccount } from './api'

// currentAccountId reads the account the access token is pinned to. The JWT
// carries the id only — the name comes from the accounts listing.
export const currentAccountId = (): string | undefined => {
  const token = authToken.getAccessToken()
  if (!token) return undefined
  try {
    return (authToken.parse(token) as { account_id?: string }).account_id
  } catch {
    return undefined
  }
}

export const fetchAccountsFx = createEffect(async () => {
  return await apiAuth.getAccounts()
})

export const renameAccountFx = createEffect(
  async ({ accountId, name }: { accountId: string; name: string }) => {
    return await apiAccount.putRenameAccount(accountId, name)
  },
)

// $accounts backs every surface that renders an account name (the org switcher,
// the rename form), so a rename is reflected everywhere without a reload.
export const $accounts = createStore<AccountOption[]>([])
  .on(fetchAccountsFx.doneData, (_, accounts) => accounts)
  .on(renameAccountFx.doneData, (accounts, renamed) =>
    accounts.map((a) =>
      a.id === renamed.id ? { ...a, name: renamed.name } : a,
    ),
  )

export const $currentAccount = $accounts.map(
  (accounts) => accounts.find((a) => a.id === currentAccountId()) ?? null,
)
