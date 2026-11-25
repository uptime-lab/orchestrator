import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import {
  deviceCapRefusal,
  isDeviceCapUnverified,
  type DeviceCapRefusal,
} from '@/entities/entitlement/deviceCap'
import { apiNodes } from '@/entities/node/api'
import type { DeletedNode } from '@/entities/node/api/deletedNodes'
import { nodeDisplayName } from '@/entities/node/utils.ts'
import { usePreferHostnames } from '@/entities/user/preferences'

import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showError, showSuccess } from '@/shared/ui/toast'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

/**
 * Deleted nodes whose devices may still be out there: the records, and the two
 * things an operator can do with one.
 *
 * Deleting a node is a one-click action whose consequence reaches past the
 * platform: the row goes, the device is told nothing, and it retries forever
 * with an identity nothing recognises. These records are the route back.
 *
 * This used to be a collapsible panel under the node list. The rows now render
 * INSIDE the list, as grey rows under the live fleet and under the unit each
 * node belonged to (`DeletedNodeCard`), because that is where an operator
 * hunting for a missing node is looking — a panel below the pager was not.
 * Clicking one opens `SidebarDeletedNode`, which is where the details and the
 * actions live, so the row stays the height and shape of every other row.
 *
 * Two hooks: the list (shared through one query key, so the page and the
 * sidebar read the same cache) and the actions with their confirmation.
 */

const QUERY_KEY = ['deleted-nodes'] as const

interface ListOptions {
  /** Whether the viewer may delete nodes; all three routes require that right. */
  canManage: boolean
  /**
   * Changes whenever the live fleet's membership changes. A delete anywhere
   * (the node panel, a batch) adds a record and a restore removes one, and
   * neither goes through this hook — the old panel fetched once on mount and
   * showed a node deleted from the panel only after a reload. Optional: the
   * sidebar has no fleet in view and reads the cache the page keeps warm.
   */
  fleetKey?: string
}

export const useDeletedNodesList = ({
  canManage,
  fleetKey = '',
}: ListOptions): { rows: DeletedNode[]; loaded: boolean } => {
  const { data, refetch, isFetched } = useQuery({
    queryKey: QUERY_KEY,
    queryFn: apiNodes.getDeletedNodes,
    enabled: canManage,
    // Silent on failure: an account with no deleted nodes is the normal case,
    // and an error banner on the node list for something nobody asked for is
    // worse than a missing grey row.
    retry: false,
  })

  // Refetch on a CHANGE of the fleet, not on mount: the query already fetches
  // once when enabled, and a second request for the same answer is noise.
  const lastFleetKey = useRef(fleetKey)
  useEffect(() => {
    if (lastFleetKey.current === fleetKey) return
    lastFleetKey.current = fleetKey
    if (canManage) void refetch()
  }, [canManage, fleetKey, refetch])

  // Deliberately NOT also filtered on serial_bound_elsewhere. "Drop the ones you
  // cannot restore" is the obvious wrong edit: if the serial was taken by
  // someone ELSE, that row is this account's only trace of where the device
  // went. The row stays, its offer does not. A row whose serial is another
  // live node of this account is not restorable and stays for the same
  // reason: it says which node to delete first.
  const rows = useMemo(
    () =>
      canManage
        ? (data ?? []).filter((r) => r.restorable || r.serial_bound_in_account)
        : [],
    [canManage, data],
  )
  return { rows, loaded: !canManage || isFetched }
}

export interface DeletedNodeActions {
  /** The row an action is in flight for, so its buttons can disable. */
  busyId: string | null
  restore: (row: DeletedNode) => void
  /** Opens the confirmation; the delete itself happens on confirm. */
  requestForget: (row: DeletedNode) => void
  /** The confirmation dialog — render it once, anywhere in the panel. */
  dialog: ReactNode
  /**
   * The device-cap refusal of the last restore, with the row it was for. A
   * restore of an admitted node is an admission, so it can meet the cap; the
   * record stays restorable, and the panel shows the add-device notice.
   */
  capRefusal: { rowId: string; refusal: DeviceCapRefusal } | null
}

interface ActionOptions {
  /** After a restore put the row back (the node list wants a refetch). */
  onRestored: (row: DeletedNode) => void
  /** After a forget removed the record. */
  onForgotten?: (row: DeletedNode) => void
}

export const useDeletedNodeActions = ({
  onRestored,
  onForgotten,
}: ActionOptions): DeletedNodeActions => {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const preferHostnames = usePreferHostnames()
  const [busyId, setBusyId] = useState<string | null>(null)
  const [pendingForget, setPendingForget] = useState<DeletedNode | null>(null)
  const [capRefusal, setCapRefusal] =
    useState<DeletedNodeActions['capRefusal']>(null)

  const refresh = useCallback(
    () => queryClient.invalidateQueries({ queryKey: QUERY_KEY }),
    [queryClient],
  )

  const restore = useCallback(
    async (row: DeletedNode) => {
      setBusyId(row.id)
      setCapRefusal(null)
      try {
        await apiNodes.restoreDeletedNode(row.id)
        const name =
          nodeDisplayName(row, preferHostnames) ||
          t('widgets.deletedNodes.fallbackName')
        // "It will reconnect" is a promise the platform cannot keep for a
        // device it asked to erase its own on-disk identity, so that case says
        // what is actually known instead.
        if (
          row.identity_release_requested &&
          row.adoption_method !== 'eeprom'
        ) {
          showSuccess(t('widgets.deletedNodes.restoredReleased', { name }))
        } else {
          showSuccess(t('widgets.deletedNodes.restored', { name }))
        }
        await refresh()
        onRestored(row)
      } catch (e) {
        const cap = deviceCapRefusal(e)
        if (cap) {
          setCapRefusal({ rowId: row.id, refusal: cap })
          return
        }
        if (isDeviceCapUnverified(e)) {
          showError(t('entities.entitlement.deviceCap.unverified'))
          return
        }
        showError(
          extractApiErrorMessage(e, t('widgets.deletedNodes.restoreError')),
        )
      } finally {
        setBusyId(null)
      }
    },
    [onRestored, preferHostnames, refresh, t],
  )

  const handleForget = useCallback(async () => {
    const row = pendingForget
    if (!row) return
    setPendingForget(null)
    setBusyId(row.id)
    try {
      await apiNodes.forgetDeletedNode(row.id)
      showSuccess(t('widgets.deletedNodes.forgotten'))
      await refresh()
      onForgotten?.(row)
    } catch (e) {
      showError(
        extractApiErrorMessage(e, t('widgets.deletedNodes.forgetError')),
      )
    } finally {
      setBusyId(null)
    }
  }, [onForgotten, pendingForget, refresh, t])

  const dialog = (
    <ConfirmDialog
      open={!!pendingForget}
      title={t('widgets.deletedNodes.confirmTitle')}
      message={
        pendingForget?.serial_bound_elsewhere
          ? t('widgets.deletedNodes.confirmMessageBoundElsewhere')
          : t('widgets.deletedNodes.confirmMessage')
      }
      confirmText={t('widgets.deletedNodes.forget')}
      destructive
      onConfirm={() => void handleForget()}
      onCancel={() => setPendingForget(null)}
    />
  )

  return {
    busyId,
    restore: (row) => void restore(row),
    requestForget: setPendingForget,
    dialog,
    capRefusal,
  }
}
