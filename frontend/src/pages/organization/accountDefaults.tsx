import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback } from 'react'
import { useTranslation } from 'react-i18next'

import { OUPolicyForm, type PolicyFormUpdate } from '@/widgets/ouPolicyForm'

import { apiOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'

// The account default policy: the last level of the chain, reaching every node
// no unit has answered for — including unassigned nodes, which no unit-level
// edit can ever reach. This page is its home (it used to hide behind a pencil
// on the Nodes rail's "Unassigned" row).
export const AccountDefaults = () => {
  const { t } = useTranslation()
  const can = useCan()
  const canManage = can(Actions.AccountPolicy)
  const queryClient = useQueryClient()

  const { data: policy, isLoading } = useQuery({
    queryKey: ['account-policy'],
    queryFn: apiOUs.getAccountPolicy,
  })

  const handleSave = useCallback(
    async (update: PolicyFormUpdate) => {
      await apiOUs.putAccountPolicy(update)
      await queryClient.invalidateQueries({ queryKey: ['account-policy'] })
    },
    [queryClient],
  )

  if (isLoading || !policy) {
    return (
      <div className="p-8 text-neutral-4">
        {t('pages.organization.defaults.loading')}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-5 max-w-5xl">
      <div className="flex flex-col gap-1">
        <h1 className="text-lg font-bold uppercase">
          {t('pages.organization.defaults.title')}
        </h1>
        <div className="text-xs text-neutral-5">
          {t('pages.organization.defaults.description')}
        </div>
      </div>
      <OUPolicyForm stored={policy} disabled={!canManage} onSave={handleSave} />
    </div>
  )
}
