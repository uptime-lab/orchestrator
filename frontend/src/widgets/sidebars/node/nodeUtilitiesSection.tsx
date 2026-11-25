import { useMutation, useQueryClient } from '@tanstack/react-query'
import clsx from 'clsx'
import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import { useOUs } from '@/entities/ou'
import {
  applyNodeUtilities,
  useNodeEffectiveUtilities,
  utilityKeys,
  type EffectiveUtility,
  type EffectiveUtilityStatus,
} from '@/entities/utility'

import { Button } from '@/shared/ui/button/button.tsx'
import { InfoHint } from '@/shared/ui/infoHint'
import {
  showError,
  showInfo,
  showSuccess,
  showWarning,
} from '@/shared/ui/toast'

// The node's EFFECTIVE utility set — what resolution (node > OU > account)
// delivers to this device — with provenance and the per-utility ladder state.
// Read-only plus one explicit "Apply now"; assignments themselves are managed
// on the Utilities page (English literals like the neighbouring
// devicePlatformSection / nodeAttributesSection).

// The catalog API is growing an optional per-utility (and per-param) `details`
// field; the entity type has not caught up yet, so extend it locally. When
// present it renders as an info mark beside the utility name.
type EffectiveUtilityRow = EffectiveUtility & { details?: string }

const statusClassName: Record<EffectiveUtilityStatus, string> = {
  declared: 'border-neutral-8/50 bg-neutral-8/10 text-neutral-5',
  update_pending: 'border-yellow-5/40 bg-yellow-5/10 text-yellow-6',
  applied: 'border-lime-7/40 bg-lime-7/10 text-lime-6',
  failed: 'border-red-6/40 bg-red-6/10 text-red-6',
}

export const NodeUtilitiesSection: FC<{
  nodeId: string
  // utility.manage — gates the Apply button; the list itself is a read.
  canManage: boolean
}> = ({ nodeId, canManage }) => {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { ous } = useOUs()
  const {
    data: utilities,
    isLoading,
    error,
  } = useNodeEffectiveUtilities(nodeId)

  const apply = useMutation({
    mutationFn: () => applyNodeUtilities(nodeId),
    onSuccess: (result) => {
      if (result.skipped) {
        // The platform's reason verbatim — it names the precondition
        // (netbooted, watcher, agent below the floor) better than a paraphrase.
        showWarning(
          t('widgets.sidebars.node.nodeUtilitiesSection.applySkipped'),
          result.reason ?? '',
        )
      } else if (result.queued) {
        showInfo(t('widgets.sidebars.node.nodeUtilitiesSection.applyQueued'))
      } else {
        showSuccess(
          t('widgets.sidebars.node.nodeUtilitiesSection.applyDispatched'),
        )
      }
      void queryClient.invalidateQueries({
        queryKey: utilityKeys.nodeEffective(nodeId),
      })
    },
    onError: (e: unknown) =>
      showError(t('widgets.sidebars.node.nodeUtilitiesSection.applyError'), e),
  })

  const provenance = (u: EffectiveUtility): string => {
    if (u.implied_by && u.implied_by.length > 0) {
      const names = u.implied_by
        .map(
          (id) =>
            (utilities ?? []).find((x) => x.utility_id === id)?.name ?? id,
        )
        .join(', ')
      return t('widgets.sidebars.node.nodeUtilitiesSection.provenanceImplied', {
        names,
      })
    }
    switch (u.source) {
      case 'node':
        return t('widgets.sidebars.node.nodeUtilitiesSection.provenanceDirect')
      case 'ou': {
        const name = ous.find((ou) => ou.id === u.source_ou_id)?.name
        return name
          ? t(
              'widgets.sidebars.node.nodeUtilitiesSection.provenanceFromNamed',
              {
                name,
              },
            )
          : t('widgets.sidebars.node.nodeUtilitiesSection.provenanceFromOU')
      }
      default:
        return t(
          'widgets.sidebars.node.nodeUtilitiesSection.provenanceAccountWide',
        )
    }
  }

  if (isLoading) {
    return (
      <div className="text-sm text-neutral-6">
        {t('widgets.sidebars.node.nodeUtilitiesSection.loading')}
      </div>
    )
  }
  if (error) {
    return (
      <div className="text-sm text-red-6">
        {t('widgets.sidebars.node.nodeUtilitiesSection.loadError')}
      </div>
    )
  }

  const list = utilities ?? []

  return (
    <div className="flex flex-col gap-4 py-2">
      {list.length === 0 ? (
        <div className="text-sm text-neutral-6">
          {t('widgets.sidebars.node.nodeUtilitiesSection.empty')}
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          {list.map((u: EffectiveUtilityRow) => {
            const statusTitle = t(
              `widgets.sidebars.node.nodeUtilitiesSection.status.${u.status}.title`,
            )
            const title =
              u.status === 'failed' && u.last_error
                ? `${statusTitle} ${u.last_error}`
                : statusTitle
            return (
              <div
                key={u.utility_id}
                className="flex items-center gap-2 text-sm text-neutral-3"
              >
                <span className="flex-1 min-w-0 flex items-center gap-1.5">
                  <span className="truncate text-neutral-2">{u.name}</span>
                  {u.details ? <InfoHint text={u.details} /> : null}
                </span>
                <span className="text-xs text-neutral-6 flex-shrink-0">
                  {provenance(u)}
                </span>
                <span
                  title={title}
                  className={clsx(
                    'flex-shrink-0 text-xxs font-medium uppercase tracking-wide px-1.5 py-0.5 rounded border',
                    statusClassName[u.status],
                  )}
                >
                  {t(
                    `widgets.sidebars.node.nodeUtilitiesSection.status.${u.status}.label`,
                  )}
                </span>
              </div>
            )
          })}
        </div>
      )}

      {canManage && list.length > 0 && (
        <Button
          variant="normal"
          grow
          disabled={apply.isPending}
          title={t('widgets.sidebars.node.nodeUtilitiesSection.applyTitle')}
          onClick={() => apply.mutate()}
        >
          {apply.isPending
            ? t('widgets.sidebars.node.nodeUtilitiesSection.applying')
            : t('widgets.sidebars.node.nodeUtilitiesSection.applyNow')}
        </Button>
      )}
    </div>
  )
}
