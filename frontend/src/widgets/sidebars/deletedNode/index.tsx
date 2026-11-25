import { useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { FC, useCallback } from 'react'
import { useTranslation } from 'react-i18next'

import {
  useDeletedNodeActions,
  useDeletedNodesList,
} from '@/widgets/deletedNodes'

import { DeviceCapNotice } from '@/entities/entitlement/ui/DeviceCapNotice'
import { nodesModel } from '@/entities/node'
import type { DeletedNode } from '@/entities/node/api/deletedNodes'
import {
  deviceStillTrying,
  restoreAffordance,
} from '@/entities/node/restoreOutlook'
import { nodeDisplayName } from '@/entities/node/utils.ts'
import { useOUs } from '@/entities/ou'
import { usePreferHostnames } from '@/entities/user/preferences'

import { Button } from '@/shared/ui/button/button.tsx'

import { SidebarCloseButton } from '../closeButton'
import { SidebarEnum, SidebarProps } from '../types'

/**
 * The panel behind a deleted node's grey row: what the node was, what a
 * restore will achieve for it, and the two actions.
 *
 * It opens from the row exactly as a live node's panel does, because a row
 * that cannot be opened reads as broken (and actions on a second line inside
 * the row would make it taller than its neighbours). The row stays a row; this is where the sentence and the
 * buttons have room.
 *
 * A restore reuses the deleted node's id, so on success the panel hands over
 * to the live node's panel under the same id — the node is back, and the
 * operator is already looking at it.
 */
const Field: FC<{
  label: string
  children: React.ReactNode
  mono?: boolean
}> = ({ label, children, mono }) => (
  <div className="flex flex-col gap-2">
    <label className="text-sm font-medium text-neutral-7 uppercase">
      {label}
    </label>
    <div
      className={clsx(
        'text-sm text-neutral-2',
        mono && 'font-mono text-neutral-4',
      )}
    >
      {children}
    </div>
  </div>
)

export const SidebarDeletedNode: FC<SidebarProps & { nodeId: string }> = ({
  nodeId,
  onClose,
}) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { ous } = useOUs()
  const preferHostnames = usePreferHostnames()
  // The renderer already refused the panel to anyone without node.delete.
  const { rows, loaded } = useDeletedNodesList({ canManage: true })
  const row = rows.find((r) => r.id === nodeId)

  const handleRestored = useCallback(
    (restored: DeletedNode) => {
      void nodesModel.fetchNodesFx()
      navigate({
        to: '.',
        search: (old) => ({
          ...old,
          sidebar: SidebarEnum.Node,
          nodeId: restored.id,
        }),
        replace: true,
      })
    },
    [navigate],
  )
  const actions = useDeletedNodeActions({
    onRestored: handleRestored,
    onForgotten: onClose,
  })

  if (!row) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        <SidebarCloseButton onClose={onClose} />
        <div className="text-neutral-4">
          {loaded
            ? t('widgets.sidebars.deletedNode.notFound')
            : t('widgets.sidebars.deletedNode.loading')}
        </div>
      </div>
    )
  }

  const affordance = restoreAffordance(row)
  const name =
    nodeDisplayName(row, preferHostnames) ||
    t('widgets.sidebars.deletedNode.fallbackName')
  const unit = row.ou_id
    ? (ous.find((o) => o.id === row.ou_id)?.name ??
      t('widgets.sidebars.deletedNode.unitGone'))
    : t('widgets.sidebars.deletedNode.unassigned')
  const busy = actions.busyId === row.id

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
      <SidebarCloseButton onClose={onClose} />

      <div className="flex flex-col gap-1">
        <h2 className="text-lg font-bold uppercase truncate" title={name}>
          {name}
        </h2>
        <span className="text-xxs uppercase tracking-wide text-neutral-7">
          {t('widgets.sidebars.deletedNode.deletedOn', {
            time: new Date(row.deleted_at).toLocaleString(),
          })}
        </span>
      </div>

      <p className="text-xs text-neutral-6">
        {t('widgets.sidebars.deletedNode.description')}
      </p>

      <Field label={t('widgets.sidebars.deletedNode.hostnameLabel')}>
        {row.hostname || '—'}
      </Field>
      <Field label={t('widgets.sidebars.deletedNode.serialLabel')} mono>
        {row.hardware_serial || t('widgets.sidebars.deletedNode.noSerial')}
      </Field>
      {/* The unit the node belonged to, and the one a restore puts it back
          into. A unit deleted meanwhile resolves as unassigned on restore. */}
      <Field label={t('widgets.sidebars.deletedNode.unitLabel')}>{unit}</Field>

      <Field label={t('widgets.sidebars.deletedNode.outlookLabel')}>
        <span
          className={
            affordance.tone === 'warning' ? 'text-orange-5' : 'text-neutral-3'
          }
        >
          {affordance.text}
        </span>
      </Field>

      {/* The device's side of the story: what it reported, and whether it is
          still knocking. Both are facts the platform observed, never verified
          — the copy says reported and refused, not erased and online. */}
      {(row.identity_released_at || row.last_refused_at) && (
        <Field label={t('widgets.sidebars.deletedNode.deviceLabel')}>
          <div className="flex flex-col gap-1">
            {row.identity_released_at && (
              <span className="text-neutral-3">
                {t('widgets.sidebars.deletedNode.releasedReported', {
                  time: new Date(row.identity_released_at).toLocaleString(),
                })}
              </span>
            )}
            {row.last_refused_at && (
              <span
                className={
                  deviceStillTrying(row) ? 'text-yellow-6' : 'text-neutral-3'
                }
              >
                {t(
                  deviceStillTrying(row)
                    ? 'widgets.sidebars.deletedNode.refusedRecently'
                    : 'widgets.sidebars.deletedNode.refusedAt',
                  { time: new Date(row.last_refused_at).toLocaleString() },
                )}
              </span>
            )}
            {row.last_refused_at && affordance.canRestore && (
              <span className="text-xxs text-neutral-6">
                {t('widgets.sidebars.deletedNode.releaseHint')}
              </span>
            )}
          </div>
        </Field>
      )}

      <div className="border-t border-neutral-11 pt-4 flex flex-col gap-3">
        {actions.capRefusal?.rowId === row.id && (
          <DeviceCapNotice refusal={actions.capRefusal.refusal} />
        )}
        {/* Disabled, never hidden: the sentence above is the reason. */}
        <Button
          size="md"
          kind="primary"
          disabled={busy || !affordance.canRestore}
          title={
            affordance.canRestore
              ? undefined
              : (affordance.blockedReason ??
                t('widgets.sidebars.deletedNode.restoreBlocked'))
          }
          onClick={() => actions.restore(row)}
        >
          {t('widgets.sidebars.deletedNode.restore')}
        </Button>
        <Button
          size="md"
          kind="secondary"
          disabled={busy}
          onClick={() => actions.requestForget(row)}
        >
          {t('widgets.sidebars.deletedNode.forget')}
        </Button>
        <p className="text-xxs text-neutral-7">
          {t('widgets.sidebars.deletedNode.forgetHint')}
        </p>
      </div>

      {actions.dialog}
    </div>
  )
}
