import { FC, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { nodesModel } from '@/entities/node'
import { useNode } from '@/entities/node/hooks.ts'
import { apiOUs, useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showError } from '@/shared/ui/toast'

import { InfoRow } from './infoRow.tsx'

// The node's organizational unit on the Overview tab: "Parent / Child" or
// Unassigned, with an in-place change for `node.assign_ou` holders. Without
// it the sidebar never names the unit at all, and moving a node is only
// reachable by dragging it onto the tree on the Nodes page.
export const NodeUnitRow: FC<{ nodeId: string }> = ({ nodeId }) => {
  const { t } = useTranslation()
  const { node } = useNode({ nodeId })
  const { ous } = useOUs()
  const check = useCan()
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  // The unit picked, awaiting confirmation. A move changes the node's policies
  // and re-applies its device login access, so it asks first.
  const [pendingTarget, setPendingTarget] = useState<string | null | undefined>(
    undefined,
  )

  const ouId = node?.ou_id ?? null
  // The move route checks the right account-wide.
  const canAssign = check(Actions.NodeAssignOU)

  // Top-level units in position order, each followed by its children.
  const orderedUnits = useMemo(() => {
    const roots = ous
      .filter((ou) => !ou.parent_id)
      .sort((a, b) => a.position - b.position)
    const out: { id: string; label: string }[] = []
    for (const root of roots) {
      out.push({ id: root.id, label: root.name })
      ous
        .filter((ou) => ou.parent_id === root.id)
        .sort((a, b) => a.position - b.position)
        .forEach((child) =>
          out.push({ id: child.id, label: `— ${child.name}` }),
        )
    }
    return out
  }, [ous])

  const unitPath = useMemo(() => {
    if (!ouId) return null
    const unit = ous.find((ou) => ou.id === ouId)
    if (!unit) return null
    const parent = unit.parent_id
      ? ous.find((ou) => ou.id === unit.parent_id)
      : undefined
    return parent ? `${parent.name} / ${unit.name}` : unit.name
  }, [ous, ouId])

  const handleChange = (next: string) => {
    const target = next === '' ? null : next
    setEditing(false)
    if (target === ouId) return
    setPendingTarget(target)
  }

  const confirmMove = async () => {
    if (pendingTarget === undefined) return
    const target = pendingTarget
    setBusy(true)
    try {
      await apiOUs.assignNodeOU(nodeId, { ou_id: target })
      await nodesModel.fetchNodesFx()
    } catch (err) {
      showError(t('widgets.sidebars.node.panel.unitRow.assignError'), err)
    } finally {
      setBusy(false)
      setPendingTarget(undefined)
    }
  }

  const targetName =
    pendingTarget === null
      ? t('widgets.sidebars.node.panel.unitRow.unassigned')
      : (ous.find((ou) => ou.id === pendingTarget)?.name ?? '')

  return (
    <InfoRow label={t('widgets.sidebars.node.panel.unitRow.label')}>
      <div className="flex items-center justify-between gap-2 h-7">
        {editing ? (
          <select
            autoFocus
            defaultValue={ouId ?? ''}
            onChange={(e) => handleChange(e.target.value)}
            onBlur={() => setEditing(false)}
            disabled={busy}
            aria-label={t('widgets.sidebars.node.panel.unitRow.selectLabel')}
            className="flex-1 min-w-0 bg-neutral-13 rounded px-2 py-1 text-sm text-neutral-1 outline-none"
          >
            <option value="">
              {t('widgets.sidebars.node.panel.unitRow.unassigned')}
            </option>
            {orderedUnits.map((unit) => (
              <option key={unit.id} value={unit.id}>
                {unit.label}
              </option>
            ))}
          </select>
        ) : (
          <>
            <span
              className={
                unitPath ? 'block truncate' : 'block truncate text-neutral-5'
              }
            >
              {unitPath ?? t('widgets.sidebars.node.panel.unitRow.unassigned')}
            </span>
            {canAssign && (
              // Same button as the Copy controls in the neighbouring rows, so
              // the column of actions reads as one.
              <Button
                size="sm"
                kind="secondary"
                onClick={() => setEditing(true)}
                disabled={busy}
                className="flex-shrink-0"
              >
                {busy
                  ? t('widgets.sidebars.node.panel.unitRow.moving')
                  : t('widgets.sidebars.node.panel.unitRow.move')}
              </Button>
            )}
          </>
        )}
      </div>
      <ConfirmDialog
        open={pendingTarget !== undefined}
        title={t('widgets.sidebars.node.panel.unitRow.confirmTitle')}
        message={
          pendingTarget === null
            ? t('widgets.sidebars.node.panel.unitRow.confirmUnassignMessage')
            : t('widgets.sidebars.node.panel.unitRow.confirmMessage', {
                unit: targetName,
              })
        }
        confirmText={t('widgets.sidebars.node.panel.unitRow.move')}
        busy={busy}
        onConfirm={() => void confirmMove()}
        onCancel={() => setPendingTarget(undefined)}
      />
    </InfoRow>
  )
}
