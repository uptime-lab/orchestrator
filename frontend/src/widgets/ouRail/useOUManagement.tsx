import { ReactNode, useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiOUs, ousModel, useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'

import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showError } from '@/shared/ui/toast'

// OU management wiring for the Organization page's rail: delete, reparent and
// reorder, plus the permission gate that turns the rail's edit mode on. It is
// the ONLY consumer — every other list page hosts a filter-only
// rail (assignment drops need no edit mode), and unit management is one click
// away on the Organization page. Returns the handler props to spread onto
// <OURail> plus a confirm-dialog node the page must render.
export const useOUManagement = (
  afterChange?: () => void,
): {
  railProps: {
    editable: boolean
    onDelete: (ouId: string) => void
    onReparent: (ouId: string, parentId: string | null) => void
    onReorder: (orderedIds: string[]) => void
  }
  dialog: ReactNode
} => {
  const { t } = useTranslation()
  const check = useCan()
  // Edit mode is the single gate that matters: with it off, create, delete,
  // reparent and reorder all go with it. The backend refuses regardless — this
  // stops offering buttons that can only 403.
  const editable = check(Actions.OUManage)
  const [pendingDeleteOU, setPendingDeleteOU] = useState<string | null>(null)
  const { ous } = useOUs()

  // A reparent moves every node of the unit under another parent's policies
  // and device login users, and the server re-applies access on each of them,
  // so it asks first, like a delete. A mis-drop must not be the whole story.
  const [pendingReparent, setPendingReparent] = useState<{
    ouId: string
    parentId: string | null
  } | null>(null)

  const onReparent = useCallback(
    (ouId: string, parentId: string | null) =>
      setPendingReparent({ ouId, parentId }),
    [],
  )

  const handleConfirmReparent = useCallback(async () => {
    if (!pendingReparent) return
    const { ouId, parentId } = pendingReparent
    try {
      await apiOUs.putUpdateOU(ouId, { parent_id: parentId ?? '' })
      await ousModel.fetchOUsFx()
      afterChange?.()
    } catch (error) {
      // Keep the current tree — and say so. The drag snapping back with no
      // message is indistinguishable from a UI bug; the server's own reason
      // ("parent would exceed the 2-level tree", "unit not found") is the
      // whole diagnostic and it was being discarded here.
      showError(t('widgets.ouRail.reparentFailed'), error)
    } finally {
      setPendingReparent(null)
    }
  }, [pendingReparent, afterChange, t])

  const onReorder = useCallback(
    async (orderedIds: string[]) => {
      try {
        await Promise.all(
          orderedIds.map((id, index) =>
            apiOUs.putUpdateOU(id, { position: index }),
          ),
        )
        await ousModel.fetchOUsFx()
        afterChange?.()
      } catch (error) {
        showError(t('widgets.ouRail.reorderFailed'), error)
      }
    },
    [afterChange],
  )

  const onDelete = useCallback((ouId: string) => setPendingDeleteOU(ouId), [])

  const handleConfirmDelete = useCallback(async () => {
    if (!pendingDeleteOU) return
    try {
      await apiOUs.deleteOU(pendingDeleteOU)
      await ousModel.fetchOUsFx()
      afterChange?.()
    } catch (error) {
      // Deletion is refused server-side when the unit still has children or
      // nodes, and the response says which — that sentence is the one thing
      // that tells the operator what to do next, so it must reach them.
      showError(t('widgets.ouRail.deleteFailed'), error)
    } finally {
      setPendingDeleteOU(null)
    }
  }, [pendingDeleteOU, afterChange])

  const nameOf = (id: string | null | undefined) =>
    ous.find((o) => o.id === id)?.name ?? ''

  const dialog = (
    <>
      <ConfirmDialog
        open={pendingDeleteOU !== null}
        title={t('widgets.ouRail.deleteTitle')}
        // The same words as the unit page's own Delete: what goes with the
        // unit is the same wherever the delete starts.
        message={t('pages.organization.unit.deleteMessage', {
          name: nameOf(pendingDeleteOU),
        })}
        confirmText={t('widgets.ouRail.deleteConfirm')}
        destructive
        onConfirm={handleConfirmDelete}
        onCancel={() => setPendingDeleteOU(null)}
      />
      <ConfirmDialog
        open={pendingReparent !== null}
        title={t('widgets.ouRail.reparentTitle')}
        message={
          pendingReparent?.parentId
            ? t('widgets.ouRail.reparentMessage', {
                name: nameOf(pendingReparent?.ouId),
                parent: nameOf(pendingReparent?.parentId),
              })
            : t('widgets.ouRail.reparentTopMessage', {
                name: nameOf(pendingReparent?.ouId),
              })
        }
        confirmText={t('widgets.ouRail.reparentConfirm')}
        onConfirm={handleConfirmReparent}
        onCancel={() => setPendingReparent(null)}
      />
    </>
  )

  return {
    railProps: { editable, onDelete, onReparent, onReorder },
    dialog,
  }
}
