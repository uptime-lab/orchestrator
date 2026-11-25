import { useNavigate, useSearch } from '@tanstack/react-router'
import { createLocator, Locator, Mark } from 'create-locator'
import { useUnit } from 'effector-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useDeletedNodesList } from '@/widgets/deletedNodes'
import {
  OU_FILTER_ALL,
  OU_FILTER_FAVORITES,
  OU_FILTER_UNASSIGNED,
} from '@/widgets/ouRail'
import { OURailAside } from '@/widgets/ouRail/OURailAside.tsx'
import { SidebarEnum } from '@/widgets/sidebars/types.ts'

import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import {
  summarizeBatchCommand,
  type BatchCommandResponse,
} from '@/entities/node/api/postBatchNodeCommand'
import type { NodeData } from '@/entities/node/api/types.ts'
import { useNodes } from '@/entities/node/hooks.ts'
import { $nodeMetrics, useFavoriteNodeIds } from '@/entities/node/model'
import {
  deletedNodeMatchesQuery,
  isNodeStatusFilter,
  matchesStatusFilter,
  NODE_STATUS_FILTERS,
  statusFilterShowsDeleted,
  type NodeStatusFilter,
} from '@/entities/node/statusFilter'
import {
  NodeList,
  NodeListLocator,
  type NodeSort,
} from '@/entities/node/ui/nodeList'
import { NODE_DND_TYPE } from '@/entities/node/ui/nodeList/nodeCard'
import {
  nodeBoard,
  nodeDisplayName,
  nodeRamGB,
  primaryDiskGB,
} from '@/entities/node/utils.ts'
import { apiOUs, useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { usePreferHostnames } from '@/entities/user/preferences'
import {
  isWorkflowActive,
  isWorkflowAvailableForOU,
  startWorkflowExecutionFx,
  useWorkflowTemplates,
} from '@/entities/workflow'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showError, showSuccess, showWarning } from '@/shared/ui/toast/model'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { isStaleTargetRefusal, pruneSelection } from './selection.ts'

// Checkbox icon with tick inside
const CheckboxIcon = () => (
  <svg
    width="16"
    height="16"
    viewBox="0 0 16 16"
    fill="none"
    xmlns="http://www.w3.org/2000/svg"
    style={{ display: 'block' }}
  >
    <rect
      x="2"
      y="2"
      width="12"
      height="12"
      rx="2"
      stroke="currentColor"
      strokeWidth="1.5"
      fill="none"
    />
    <path
      d="M5 8 L7 10 L11 6"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      fill="none"
    />
  </svg>
)

// Six-dot grab handle — signals that the selection is draggable.

// A row inside the batch actions dropdown.
const BatchMenuRow = ({
  label,
  accent,
  onClick,
}: {
  label: string
  accent?: 'violet' | 'red'
  onClick: () => void
}) => (
  <button
    type="button"
    onClick={onClick}
    className={
      'w-full text-left px-3 py-1.5 font-mono text-xs transition-colors hover:bg-neutral-11 ' +
      (accent === 'red'
        ? 'text-neutral-5 hover:text-red-6'
        : accent === 'violet'
          ? 'text-neutral-5 hover:text-violet-5'
          : 'text-neutral-4 hover:text-lime-6')
    }
  >
    {label}
  </button>
)

export type HomePageLocator = Locator<{
  nodes: NodeListLocator
}>

type HomePageProps = Mark<HomePageLocator>

export const HomePage = ({ ...rest }: HomePageProps) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = useSearch({ from: '/_mainLayout' })
  const locator = createLocator(rest)
  const { nodes } = useNodes()
  // Bulk actions are account-wide (they span nodes across OUs), so gate them
  // on account-wide grants; an OU-scoped admin uses per-node actions instead.
  const check = useCan()
  const canBatchReboot = check(Actions.NodeReboot)
  const canBatchRecovery = check(Actions.NodeRebootRecovery)
  const canBatchShutdown = check(Actions.NodeShutdown)
  const canBatchDelete = check(Actions.NodeDelete)
  const canBatchPlaybook = check(Actions.PlaybookRun)
  // Assigning the selection to a unit is the one bulk action that is not a
  // device command; it gets its own menu in the selection toolbar because
  // drag-onto-the-rail was the only way to do it and nobody found it.
  const canBatchAssign = check(Actions.NodeAssignOU)
  const canAnyBatch =
    canBatchReboot ||
    canBatchRecovery ||
    canBatchShutdown ||
    canBatchDelete ||
    canBatchPlaybook
  const { ous } = useOUs()
  const { templates, fetchTemplates } = useWorkflowTemplates()

  // OU tree filter selection persisted in the URL ('all' by default).
  const ouSelection =
    'ou' in search && typeof search.ou === 'string' ? search.ou : OU_FILTER_ALL

  const favoriteNodeIds = useFavoriteNodeIds()

  // A node whose unit no longer exists (restored after its unit was deleted)
  // belongs to no unit the rail can show, so it is listed as unassigned.
  const knownOUIds = useMemo(() => new Set(ous.map((ou) => ou.id)), [ous])
  const isUnassigned = useCallback(
    (node: { ou_id?: string | null }) =>
      !node.ou_id || (knownOUIds.size > 0 && !knownOUIds.has(node.ou_id)),
    [knownOUIds],
  )

  // Client-side filtering of the already-loaded node list by OU selection.
  const ouFilteredNodes = useMemo(() => {
    if (ouSelection === OU_FILTER_ALL) {
      return nodes
    }
    if (ouSelection === OU_FILTER_FAVORITES) {
      return nodes.filter((node) => favoriteNodeIds.has(node.id))
    }
    if (ouSelection === OU_FILTER_UNASSIGNED) {
      return nodes.filter(isUnassigned)
    }
    // A root OU matches its whole branch (itself + nested OUs); a nested OU
    // matches only its own id.
    const childIds = ous
      .filter((ou) => ou.parent_id === ouSelection)
      .map((ou) => ou.id)
    const branch = new Set<string>([ouSelection, ...childIds])
    return nodes.filter((node) => node.ou_id && branch.has(node.ou_id))
  }, [nodes, ous, ouSelection, favoriteNodeIds, isUnassigned])

  // Text + status filters on top of the OU selection.
  const [searchQuery, setSearchQuery] = useState('')
  // What each option means lives in entities/node/statusFilter; this page only
  // applies it.
  const [statusFilter, setStatusFilter] = useState<NodeStatusFilter>('all')
  // Hardware filters (client-side over the loaded set), mirroring the new
  // Board/RAM/Disk columns. Values are threshold buckets so the control stays
  // a simple select rather than a range slider.
  // 'all' or any BoardLabel. Not a hardcoded union any more: the options are
  // derived from the boards actually present, so a newly supported board shows
  // up without a frontend change — the old 'CM4' | 'CM5' union silently hid
  // every Pi 4 and Pi 5 from this filter.
  const [boardFilter, setBoardFilter] = useState<string>('all')
  const [ramFilter, setRamFilter] = useState<'all' | '4' | '8' | '16'>('all')
  const [diskFilter, setDiskFilter] = useState<
    'all' | '128' | '256' | '512' | '1000'
  >('all')

  // Boards actually present in this account, so the filter never offers an
  // empty result and never omits a board the fleet contains.
  const boardOptions = useMemo(() => {
    const seen = new Set<string>()
    for (const n of ouFilteredNodes) {
      const b = nodeBoard(n)
      if (b) seen.add(b)
    }
    return [...seen].sort()
  }, [ouFilteredNodes])

  const filteredNodes = useMemo(() => {
    const query = searchQuery.trim().toLowerCase()
    const ramMin = ramFilter === 'all' ? 0 : Number(ramFilter)
    const diskMin = diskFilter === 'all' ? 0 : Number(diskFilter)
    return ouFilteredNodes.filter((node) => {
      if (!matchesStatusFilter(node, statusFilter)) return false
      if (boardFilter !== 'all' && nodeBoard(node) !== boardFilter) return false
      if (ramMin > 0) {
        const gb = nodeRamGB(node)
        if (gb === null || gb < ramMin) return false
      }
      if (diskMin > 0) {
        const gb = primaryDiskGB(node)
        if (gb === null || gb < diskMin) return false
      }
      if (!query) return true
      const haystack = [
        node.name,
        node.hostname,
        node.ipv4,
        node.processor_serial,
        node.system_info?.os?.details,
        node.system_info?.os?.version,
        // Operator notes: the only searchable field the device never reports,
        // which is what makes "which node is in rack 3" answerable at all.
        node.notes,
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
      return haystack.includes(query)
    })
  }, [
    ouFilteredNodes,
    searchQuery,
    statusFilter,
    boardFilter,
    ramFilter,
    diskFilter,
  ])

  // Column sorting. Live-metric sorts read the latest WebSocket sample, so
  // the order updates dynamically as new samples arrive. Nodes without a
  // value for the active key always sink to the bottom.
  const [sort, setSort] = useState<NodeSort | null>(null)
  const metricsByNode = useUnit($nodeMetrics)
  const preferHostnames = usePreferHostnames()

  const handleSortChange = useCallback((key: string) => {
    setSort((prev) => {
      if (!prev || prev.key !== key) return { key, dir: 'asc' }
      if (prev.dir === 'asc') return { key, dir: 'desc' }
      return null // third click clears the sort
    })
  }, [])

  const sortedNodes = useMemo(() => {
    if (!sort) return filteredNodes
    const GB = 1024 * 1024 * 1024
    const latest = (id: string) => {
      const history = metricsByNode[id]
      return history?.[history.length - 1]
    }
    const value = (n: NodeData): string | number | undefined => {
      switch (sort.key) {
        // Favourites first on the FIRST click, which is what an operator means
        // by sorting on this column — hence 0 for a favourite, not 1. Never
        // undefined: "not a favourite" is an answer, and undefined would sink
        // those rows in both directions and make the descending sort a no-op.
        case 'favorite':
          return favoriteNodeIds.has(n.id) ? 0 : 1
        case 'name':
          // The column shows the display name, so it must sort by it too —
          // with "prefer hostnames" on, that is the hostname.
          return nodeDisplayName(n, preferHostnames).toLowerCase() || undefined
        case 'hostname':
          return n.hostname?.toLowerCase() || undefined
        case 'address':
          return n.ipv4 || undefined
        case 'unit': {
          if (!n.ou_id) return undefined
          return ous.find((ou) => ou.id === n.ou_id)?.name.toLowerCase()
        }
        case 'os':
          return (
            n.system_info?.os?.details ||
            n.system_info?.os?.version ||
            undefined
          )?.toLowerCase()
        case 'uptime': {
          const bootedAt = n.system_info?.hardware?.boot?.booted_at
          return bootedAt ? Date.now() / 1000 - bootedAt : undefined
        }
        case 'storage': {
          const devices =
            n.system_info?.storage_devices ?? n.storage_devices ?? {}
          const total = Object.values(devices).reduce(
            (sum, d) => sum + (d.total_capacity || 0),
            0,
          )
          return total > 0 ? total / GB : undefined
        }
        case 'board':
          return nodeBoard(n) ?? undefined
        case 'ram':
          return nodeRamGB(n) ?? undefined
        case 'disk':
          return primaryDiskGB(n) ?? undefined
        case 'cpu':
          return latest(n.id)?.cpu_percent
        case 'memory': {
          const m = latest(n.id)
          return m && m.mem_total_bytes > 0
            ? m.mem_used_bytes / m.mem_total_bytes
            : undefined
        }
        case 'temp': {
          const thermal = latest(n.id)?.thermal
          return thermal?.length
            ? Math.max(...thermal.map((z) => z.temp_celsius))
            : undefined
        }
        default:
          return undefined
      }
    }
    const factor = sort.dir === 'asc' ? 1 : -1
    return [...filteredNodes].sort((a, b) => {
      const va = value(a)
      const vb = value(b)
      if (va === undefined && vb === undefined) return 0
      if (va === undefined) return 1 // missing values sink regardless of dir
      if (vb === undefined) return -1
      if (typeof va === 'string' && typeof vb === 'string') {
        return factor * va.localeCompare(vb)
      }
      return factor * (Number(va) - Number(vb))
    })
  }, [
    filteredNodes,
    sort,
    metricsByNode,
    ous,
    favoriteNodeIds,
    preferHostnames,
  ])

  // Deleted nodes whose devices may still be out there. They render as grey
  // rows AFTER the live fleet — always last, whatever the sort, because they
  // are not nodes competing on a metric but a footer of things that used to be
  // — and go through the same OU, status and search filters, so a deleted node
  // still shows under the unit it belonged to. Every hardware filter excludes
  // them: a tombstone has no board, RAM or disk to match on.
  //
  // Only for a viewer who may delete nodes; the three routes require that
  // right and the hook answers an empty list otherwise.
  const fleetKey = useMemo(
    () =>
      nodes
        .map((n) => n.id)
        .sort()
        .join(','),
    [nodes],
  )
  const deleted = useDeletedNodesList({
    canManage: canBatchDelete,
    fleetKey,
  })
  const filteredDeleted = useMemo(() => {
    if (!statusFilterShowsDeleted(statusFilter)) return []
    if (boardFilter !== 'all' || ramFilter !== 'all' || diskFilter !== 'all') {
      return []
    }
    const query = searchQuery.trim().toLowerCase()
    return deleted.rows.filter((row) => {
      if (ouSelection === OU_FILTER_FAVORITES) return false
      if (ouSelection === OU_FILTER_UNASSIGNED && row.ou_id) return false
      if (
        ouSelection !== OU_FILTER_ALL &&
        ouSelection !== OU_FILTER_UNASSIGNED
      ) {
        const childIds = ous
          .filter((ou) => ou.parent_id === ouSelection)
          .map((ou) => ou.id)
        const branch = new Set<string>([ouSelection, ...childIds])
        if (!row.ou_id || !branch.has(row.ou_id)) return false
      }
      return deletedNodeMatchesQuery(row, query)
    })
  }, [
    deleted.rows,
    statusFilter,
    boardFilter,
    ramFilter,
    diskFilter,
    searchQuery,
    ouSelection,
    ous,
  ])

  // Pagination: page size persisted, current page clamped to the result set.
  // Pages run over live rows FOLLOWED BY deleted rows, as one sequence, so the
  // deleted rows land on the last page rather than trailing every page.
  const [pageSize, setPageSize] = useState(() => {
    const stored = Number(localStorage.getItem('nodes-page-size'))
    return [10, 20, 50, 100].includes(stored) ? stored : 20
  })
  const [page, setPage] = useState(0)
  const totalRows = sortedNodes.length + filteredDeleted.length
  const pageCount = Math.max(1, Math.ceil(totalRows / pageSize))
  const currentPage = Math.min(page, pageCount - 1)
  const pagedNodes = useMemo(
    () =>
      sortedNodes.slice(currentPage * pageSize, (currentPage + 1) * pageSize),
    [sortedNodes, currentPage, pageSize],
  )
  const pagedDeleted = useMemo(() => {
    // Offsets into the deleted list are relative to where the live rows end.
    const from = currentPage * pageSize - sortedNodes.length
    const to = from + pageSize
    if (to <= 0) return []
    return filteredDeleted.slice(Math.max(0, from), to)
  }, [filteredDeleted, sortedNodes.length, currentPage, pageSize])
  // Whether an empty result is the filters' doing, so the list can say so
  // instead of suggesting the account add its first node.
  const filtersActive =
    statusFilter !== 'all' ||
    boardFilter !== 'all' ||
    ramFilter !== 'all' ||
    diskFilter !== 'all' ||
    searchQuery.trim() !== ''

  const handlePageSizeChange = useCallback((size: number) => {
    localStorage.setItem('nodes-page-size', String(size))
    setPageSize(size)
    setPage(0)
  }, [])

  // Extract nodeId from search params for highlighting. Every node view now
  // lives inside the node panel itself, so the node sidebar is the only one
  // carrying a nodeId — plus the deleted node's panel, which carries the
  // DELETED node's id and highlights its grey row instead.
  const selectedNodeId =
    search.sidebar === SidebarEnum.Node && 'nodeId' in search
      ? search.nodeId
      : undefined
  const selectedDeletedNodeId =
    search.sidebar === SidebarEnum.DeletedNode && 'nodeId' in search
      ? search.nodeId
      : undefined

  // Multiselect state
  const [isMultiselectMode, setIsMultiselectMode] = useState(false)
  const [selectedNodeIds, setSelectedNodeIds] = useState<Set<string>>(new Set())
  const [batchDeleteError, setBatchDeleteError] = useState<string | null>(null)
  // Nodes drag only while selecting — never in the plain list. The rail here
  // is a filter (unit management lives on the Organization page), so
  // multiselect is the one mode that makes a node draggable.
  const nodesDraggable = isMultiselectMode

  // Fetch templates on mount
  useEffect(() => {
    if (templates.length === 0) {
      fetchTemplates()
    }
  }, [templates.length, fetchTemplates])

  const handleOpenNode = useCallback(
    (nodeId: string) => {
      if (isMultiselectMode) {
        return
      }
      // Clicking the already-open node again closes the panel (toggle).
      if (search.sidebar === SidebarEnum.Node && selectedNodeId === nodeId) {
        navigate({
          to: '.',
          search: (old) => {
            const {
              sidebar: _sidebar,
              nodeId: _nodeId,
              ...next
            } = old as Record<string, unknown>
            return next
          },
          replace: true,
        })
        return
      }
      navigate({
        to: '.',
        search: (old) => ({
          ...old,
          sidebar: SidebarEnum.Node,
          nodeId,
        }),
      })
    },
    [navigate, isMultiselectMode, search.sidebar, selectedNodeId],
  )

  // A deleted node's grey row opens its own panel (details, restore, forget),
  // toggling like a live row. No multiselect guard: those rows carry no
  // checkbox and are never part of a selection.
  const handleOpenDeletedNode = useCallback(
    (nodeId: string) => {
      if (
        search.sidebar === SidebarEnum.DeletedNode &&
        selectedDeletedNodeId === nodeId
      ) {
        navigate({
          to: '.',
          search: (old) => {
            const {
              sidebar: _sidebar,
              nodeId: _nodeId,
              ...next
            } = old as Record<string, unknown>
            return next
          },
          replace: true,
        })
        return
      }
      navigate({
        to: '.',
        search: (old) => ({
          ...old,
          sidebar: SidebarEnum.DeletedNode,
          nodeId,
        }),
      })
    },
    [navigate, search.sidebar, selectedDeletedNodeId],
  )

  // Open the unified Add Node sidebar directly on its tab menu (Single by
  // default; the sidebar's own tab bar switches to SSH / Master image).
  const handleAddNode = useCallback(() => {
    navigate({
      to: '.',
      search: (old) => ({
        ...old,
        sidebar: SidebarEnum.CreateNode,
        view: undefined,
      }),
    })
  }, [navigate])

  const handleSelectOU = useCallback(
    (value: string) => {
      navigate({
        to: '.',
        search: (old) => ({
          ...old,
          ou: value === OU_FILTER_ALL ? undefined : value,
        }),
        replace: true,
      })
    },
    [navigate],
  )

  // Unassigned nodes follow the ACCOUNT policy; its editor is the Organization
  // page's "Account defaults".
  // Per-OU node counts + the pseudo-rows (All / Favorites / Unassigned) for
  // the shared OU rail.
  const ouNodeCounts = useMemo(() => {
    const counts: Record<string, number> = {}
    nodes.forEach((node) => {
      if (node.ou_id) counts[node.ou_id] = (counts[node.ou_id] ?? 0) + 1
    })
    return counts
  }, [nodes])
  const nodePseudoRows = useMemo(
    () => [
      {
        value: OU_FILTER_ALL,
        label: t('widgets.ouRail.allDevices'),
        count: nodes.length,
      },
      {
        value: OU_FILTER_FAVORITES,
        label: t('widgets.ouRail.favorites'),
        count: nodes.filter((node) => favoriteNodeIds.has(node.id)).length,
      },
      {
        value: OU_FILTER_UNASSIGNED,
        label: t('widgets.ouRail.unassigned'),
        count: nodes.filter(isUnassigned).length,
        dropTarget: null,
      },
    ],
    [nodes, favoriteNodeIds, t, isUnassigned],
  )

  // Drop target on the OU tree: assign the dragged node(s) and refresh the list
  // so counts and the active filter update. The payload is a comma-separated id
  // list (one, or the whole selection when dragged from multiselect).
  const handleAssignNode = useCallback(
    async (nodeIds: string, ouId: string | null) => {
      const ids = nodeIds.split(',').filter(Boolean)
      if (ids.length === 0) return
      // Settled per node rather than Promise.all: a drop can carry the whole
      // selection, and one refusal used to abandon the outcome of the rest. It
      // was then swallowed entirely — "keep the current list; the assignment
      // simply failed to apply" — so a drag that moved nothing looked exactly
      // like a drag that moved everything, on an interaction that is already
      // easy to misread.
      const settled = await Promise.all(
        ids.map(async (nodeId) => {
          try {
            await apiOUs.assignNodeOU(nodeId, { ou_id: ouId })
            return { nodeId, ok: true, error: '' }
          } catch (e) {
            return {
              nodeId,
              ok: false,
              error: extractApiErrorMessage(e, ''),
            }
          }
        }),
      )
      await nodesModel.fetchNodesFx()

      const failed = settled.filter((r) => !r.ok)
      const target = ouId
        ? ous.find((o) => o.id === ouId)?.name ||
          t('pages.home.assign.fallbackUnit')
        : t('widgets.ouRail.unassigned')
      if (failed.length === 0) {
        // A single-row drag is its own feedback — the row visibly moves — so
        // only a group drop is worth a toast.
        if (ids.length > 1) {
          showSuccess(
            t('pages.home.assign.movedSuccess', { total: ids.length, target }),
          )
        }
        return
      }
      showError(
        t('pages.home.assign.movedPartial', {
          moved: ids.length - failed.length,
          total: ids.length,
          target,
          failed: failed.length,
          names: failed
            .slice(0, 4)
            .map((r) => nodes.find((n) => n.id === r.nodeId)?.name || r.nodeId)
            .join(', '),
        }) +
          (failed.length > 4 ? t('pages.home.assign.andMore') : '') +
          (failed[0].error
            ? t('pages.home.assign.reasonSuffix', { reason: failed[0].error })
            : ''),
      )
    },
    [nodes, ous, t],
  )

  const handleToggleMultiselect = useCallback(() => {
    setIsMultiselectMode(!isMultiselectMode)
    setSelectedNodeIds(new Set())
  }, [isMultiselectMode])

  const handleSelectNode = useCallback((nodeId: string) => {
    setSelectedNodeIds((prev) => {
      const newSet = new Set(prev)
      if (newSet.has(nodeId)) {
        newSet.delete(nodeId)
      } else {
        newSet.add(nodeId)
      }
      return newSet
    })
  }, [])

  const handleSelectAll = useCallback(() => {
    const allNodeIds = filteredNodes.map((node) => node.id)
    const allSelected =
      allNodeIds.length > 0 && allNodeIds.every((id) => selectedNodeIds.has(id))

    if (allSelected) {
      setSelectedNodeIds(new Set())
    } else {
      setSelectedNodeIds(new Set(allNodeIds))
    }
  }, [filteredNodes, selectedNodeIds])

  // reportBatchOutcome exists because the batch endpoint
  // answers 200 for a per-node refusal and a refused node gets no node_commands
  // row either, so no WebSocket event exposes it. If this does not say what
  // happened, nothing does.
  //
  // Declared ABOVE its callers on purpose — a useCallback dependency array is
  // evaluated where it is written, so referencing this from a hook defined
  // earlier in the component body is a temporal-dead-zone crash, not a warning.
  const reportBatchOutcome = useCallback(
    (action: string, result: BatchCommandResponse) => {
      const label = (nodeId: string) =>
        nodes.find((n) => n.id === nodeId)?.name || nodeId
      const outcome = summarizeBatchCommand(action, result, label)
      if (outcome.tone === 'success') showSuccess(outcome.headline)
      else if (outcome.tone === 'warning')
        showWarning(outcome.headline, outcome.detail)
      // showError's second argument is a caught error of unknown shape, so the
      // detail travels inside the message rather than as something to unwrap.
      else showError(`${outcome.headline}. ${outcome.detail ?? ''}`.trim())
    },
    [nodes],
  )

  const handleBatchDelete = useCallback(async () => {
    if (selectedNodeIds.size === 0) return

    // force: the confirmation above states what deleting an online device does,
    // which is the acknowledgement the platform's gate asks for. Without it
    // every connected node in the selection would come back 409 and the batch
    // would look broken.
    //
    // Each delete is settled individually rather than through Promise.all: a
    // rejection there abandons the remaining promises' outcomes, so a batch
    // where one node refused reported one message and said nothing about the
    // eleven that were deleted — on the most destructive action on this page.
    const ids = pruneSelection(selectedNodeIds, nodes).kept
    const settled = await Promise.all(
      ids.map(async (id) => {
        try {
          await apiNodes.deleteNode(id, true)
          return { node_id: id, success: true }
        } catch (e) {
          return {
            node_id: id,
            success: false,
            error: extractApiErrorMessage(e, t('pages.home.batch.refused')),
          }
        }
      }),
    )
    await nodesModel.fetchNodesFx()

    const failed = settled.filter((r) => !r.success)
    if (failed.length === 0) {
      setIsMultiselectMode(false)
      setSelectedNodeIds(new Set())
      setBatchDeleteError(null)
    } else {
      // Keep the selection: the operator's next move is to retry or inspect the
      // nodes that refused, and clearing it would make them hunt for the list.
      setSelectedNodeIds(new Set(failed.map((r) => r.node_id)))
    }
    reportBatchOutcome(t('pages.home.batch.delete'), {
      results: settled,
      total_count: settled.length,
      success_count: settled.length - failed.length,
      failure_count: failed.length,
      queued_count: 0,
      skipped_count: 0,
    })
  }, [selectedNodeIds, nodes, reportBatchOutcome, t])

  // Batch actions menu: one confirm dialog covers every batch operation.
  const [batchMenuOpen, setBatchMenuOpen] = useState(false)
  const [assignMenuOpen, setAssignMenuOpen] = useState(false)

  // Top-level units in position order, each followed by its children — the
  // flat list the "Assign to unit" menu renders.
  const orderedUnits = useMemo(() => {
    const roots = ous
      .filter((ou) => !ou.parent_id)
      .sort((a, b) => a.position - b.position)
    const out: { id: string; label: string; nested: boolean }[] = []
    for (const root of roots) {
      out.push({ id: root.id, label: root.name, nested: false })
      ous
        .filter((ou) => ou.parent_id === root.id)
        .sort((a, b) => a.position - b.position)
        .forEach((child) =>
          out.push({ id: child.id, label: child.name, nested: true }),
        )
    }
    return out
  }, [ous])

  const handleAssignSelection = useCallback(
    (ouId: string | null) => {
      setAssignMenuOpen(false)
      if (selectedNodeIds.size === 0) return
      void handleAssignNode(Array.from(selectedNodeIds).join(','), ouId)
    },
    [selectedNodeIds, handleAssignNode],
  )
  const [batchConfirm, setBatchConfirm] = useState<
    | null
    | { kind: 'reboot' | 'recovery' | 'shutdown' | 'delete' }
    | { kind: 'workflow'; templateId: string }
  >(null)

  const handleBatchConfirm = useCallback(async () => {
    if (!batchConfirm) return
    setBatchConfirm(null)
    setBatchDeleteError(null)
    const { kept: ids, dropped } = pruneSelection(selectedNodeIds, nodes)
    if (dropped > 0) {
      setSelectedNodeIds(new Set(ids))
      if (ids.length === 0) {
        setBatchDeleteError(t('pages.home.batch.selectionGone'))
        return
      }
      showWarning(t('pages.home.batch.selectionPruned', { count: dropped }))
    }
    try {
      switch (batchConfirm.kind) {
        case 'reboot':
          reportBatchOutcome(
            t('pages.home.batch.reboot'),
            await apiNodes.postBatchNodeCommand({
              node_ids: ids,
              command: 'reboot',
              params: { delay_seconds: 0 },
            }),
          )
          break
        case 'shutdown':
          reportBatchOutcome(
            t('pages.home.batch.shutdown'),
            await apiNodes.postBatchNodeCommand({
              node_ids: ids,
              command: 'shutdown',
              params: { delay_seconds: 0 },
            }),
          )
          break
        case 'recovery': {
          // allSettled, not all: reboot-to-recovery is a per-node endpoint that
          // THROWS on a refusal, so Promise.all would abort on the first one and
          // the catch below would report a single message for a batch where the
          // other eleven nodes were accepted.
          const settled = await Promise.all(
            ids.map(async (id) => {
              try {
                await apiNodes.postRebootToRecovery(id)
                return { node_id: id, success: true }
              } catch (e) {
                return {
                  node_id: id,
                  success: false,
                  error: extractApiErrorMessage(
                    e,
                    t('pages.home.batch.refused'),
                  ),
                }
              }
            }),
          )
          reportBatchOutcome(t('pages.home.batch.sendToRecovery'), {
            results: settled,
            total_count: settled.length,
            success_count: settled.filter((r) => r.success).length,
            failure_count: settled.filter((r) => !r.success).length,
            queued_count: 0,
            skipped_count: 0,
          })
          break
        }
        case 'delete':
          await handleBatchDelete()
          return
        case 'workflow': {
          const execution = await startWorkflowExecutionFx({
            template_id: batchConfirm.templateId,
            node_ids: ids,
          })
          await nodesModel.fetchNodesFx()
          navigate({
            to: '/workflows',
            search: {
              sidebar: 'workflowExecutionDetails',
              executionId: execution.id,
              tab: 'runs',
            } as never,
          })
          return
        }
      }
      await nodesModel.fetchNodesFx()
    } catch (err) {
      // A node deleted after the pruning above: re-read the list so the
      // selection drops it, and say that is why nothing started.
      if (batchConfirm.kind === 'workflow' && isStaleTargetRefusal(err)) {
        const fresh = await nodesModel.fetchNodesFx().catch(() => null)
        if (fresh) {
          setSelectedNodeIds(new Set(pruneSelection(ids, fresh).kept))
        }
        setBatchDeleteError(t('pages.home.batch.selectionChanged'))
        return
      }
      // Surface the server's reason (e.g. a 403 scope refusal), not the
      // generic axios "Request failed with status code NNN".
      setBatchDeleteError(
        t('pages.home.batch.failed', {
          message: extractApiErrorMessage(
            err,
            t('pages.home.batch.unknownError'),
          ),
        }),
      )
    }
  }, [
    batchConfirm,
    selectedNodeIds,
    nodes,
    handleBatchDelete,
    navigate,
    reportBatchOutcome,
    t,
  ])

  const batchConfirmText = useMemo(() => {
    if (!batchConfirm) return { title: '', message: '' }
    const count = selectedNodeIds.size
    const suffix = t('pages.home.batch.confirm.selectedNodes', { count })
    switch (batchConfirm.kind) {
      case 'reboot':
        return {
          title: t('pages.home.batch.confirm.rebootTitle'),
          message: t('pages.home.batch.confirm.rebootMessage', {
            nodes: suffix,
          }),
        }
      case 'recovery':
        return {
          title: t('pages.home.batch.confirm.recoveryTitle'),
          message: t('pages.home.batch.confirm.recoveryMessage', {
            nodes: suffix,
          }),
        }
      case 'shutdown':
        return {
          title: t('pages.home.batch.confirm.shutdownTitle'),
          message: t('pages.home.batch.confirm.shutdownMessage', {
            nodes: suffix,
          }),
        }
      case 'delete':
        return {
          title: t('pages.home.batch.confirm.deleteTitle'),
          // Same honesty as the single-node dialog: the record goes and the
          // devices are not told, so each one retries into nothing until someone
          // restores it. Any that are ONLINE are additionally asked to forget
          // they were adopted, which is what the force below means.
          message: t('pages.home.batch.confirm.deleteMessage', {
            nodes: suffix,
          }),
        }
      case 'workflow': {
        const name =
          templates.find((tpl) => tpl.id === batchConfirm.templateId)?.name ??
          ''
        return {
          title: t('pages.home.batch.confirm.workflowTitle'),
          message: t('pages.home.batch.confirm.workflowMessage', {
            name,
            nodes: suffix,
          }),
        }
      }
    }
  }, [batchConfirm, selectedNodeIds, templates, t])

  // Batch menu offers only active playbooks available to EVERY selected
  // node's OU.
  const batchTemplates = useMemo(() => {
    const selectedNodes = nodes.filter((node) => selectedNodeIds.has(node.id))
    return templates.filter(
      (template) =>
        isWorkflowActive(template) &&
        selectedNodes.every((node) =>
          isWorkflowAvailableForOU(template, node.ou_id, ous),
        ),
    )
  }, [templates, nodes, selectedNodeIds, ous])

  const allNodeIds = useMemo(
    () => filteredNodes.map((node) => node.id),
    [filteredNodes],
  )
  const allSelected = useMemo(
    () =>
      allNodeIds.length > 0 &&
      allNodeIds.every((id) => selectedNodeIds.has(id)),
    [allNodeIds, selectedNodeIds],
  )

  return (
    <div className="flex h-full w-full" {...locator()}>
      {/* Left OU tree filter panel (collapsible) */}
      <OURailAside
        counts={ouNodeCounts}
        selected={ouSelection}
        onSelect={handleSelectOU}
        pseudoRows={nodePseudoRows}
        dropTypes={[NODE_DND_TYPE]}
        onDropItem={(nodeId, ouId) => handleAssignNode(nodeId, ouId)}
        // Drops are accepted while a selection is being made — the only time
        // a node is draggable on this page.
        alwaysAcceptDrops={isMultiselectMode}
        dropHint={t('pages.home.ouDropHint')}
      />

      {/* Device list */}
      <div className="flex-1 min-w-0 h-full overflow-y-auto p-4 pt-8 md:p-8">
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <h1 className="text-base md:text-lg uppercase font-bold whitespace-nowrap">
              {t('pages.home.title')}
            </h1>
            <button
              type="button"
              onClick={handleToggleMultiselect}
              className="flex items-center gap-1.5 px-2 py-1 text-xs uppercase tracking-wider text-neutral-6 hover:text-neutral-2 hover:bg-neutral-13 rounded transition-colors"
              aria-label={
                isMultiselectMode
                  ? t('pages.home.multiselect.disable')
                  : t('pages.home.multiselect.enable')
              }
              title={
                isMultiselectMode
                  ? t('pages.home.multiselect.disable')
                  : t('pages.home.multiselect.enable')
              }
            >
              <CheckboxIcon />
              {isMultiselectMode
                ? t('pages.home.multiselect.done')
                : t('pages.home.multiselect.select')}
            </button>
            {isMultiselectMode && (
              <>
                <Button size="md" kind="secondary" onClick={handleSelectAll}>
                  {allSelected
                    ? t('pages.home.multiselect.deselectAll')
                    : t('pages.home.multiselect.selectAll')}
                </Button>

                {/* Assign the selection to a unit — the explicit path; dragging
                  onto the rail stays as the shortcut. */}
                {canBatchAssign && (
                  <div className="relative">
                    <Button
                      size="md"
                      kind="secondary"
                      onClick={() => setAssignMenuOpen((v) => !v)}
                      disabled={selectedNodeIds.size < 1}
                    >
                      {t('pages.home.multiselect.assignToUnit')}
                    </Button>
                    {assignMenuOpen && (
                      <>
                        <div
                          className="fixed inset-0 z-40"
                          onClick={() => setAssignMenuOpen(false)}
                        />
                        <div className="absolute left-0 mt-1 z-50 bg-neutral-12 rounded py-1 min-w-56 max-h-80 overflow-y-auto shadow-lg text-left">
                          {orderedUnits.length === 0 && (
                            <div className="px-3 py-1.5 text-xs text-neutral-6">
                              {t('pages.home.multiselect.noUnits')}
                            </div>
                          )}
                          {orderedUnits.map((unit) => (
                            <BatchMenuRow
                              key={unit.id}
                              label={
                                unit.nested ? `— ${unit.label}` : unit.label
                              }
                              onClick={() => handleAssignSelection(unit.id)}
                            />
                          ))}
                          <div className="mt-1 pt-1 border-t border-neutral-11">
                            <BatchMenuRow
                              label={t('pages.home.multiselect.removeFromUnit')}
                              onClick={() => handleAssignSelection(null)}
                            />
                          </div>
                        </div>
                      </>
                    )}
                  </div>
                )}

                {/* Batch actions: one menu mirroring the single-node menu.
                  Hidden entirely for users with no bulk permissions. */}
                {canAnyBatch && (
                  <div className="relative">
                    <Button
                      size="md"
                      kind="primary"
                      onClick={() => setBatchMenuOpen((v) => !v)}
                      disabled={selectedNodeIds.size < 1}
                    >
                      {t('pages.home.batch.actionsButton', {
                        selected: selectedNodeIds.size,
                      })}
                    </Button>
                    {batchMenuOpen && (
                      <>
                        <div
                          className="fixed inset-0 z-40"
                          onClick={() => setBatchMenuOpen(false)}
                        />
                        <div className="absolute left-0 mt-1 z-50 bg-neutral-12 rounded py-1 min-w-56 shadow-lg text-left">
                          {canBatchReboot && (
                            <BatchMenuRow
                              label={t('pages.home.batch.reboot')}
                              onClick={() => {
                                setBatchMenuOpen(false)
                                setBatchConfirm({ kind: 'reboot' })
                              }}
                            />
                          )}
                          {canBatchRecovery && (
                            <BatchMenuRow
                              label={t('pages.home.batch.rebootToRecovery')}
                              accent="violet"
                              onClick={() => {
                                setBatchMenuOpen(false)
                                setBatchConfirm({ kind: 'recovery' })
                              }}
                            />
                          )}
                          {canBatchPlaybook && (
                            <>
                              <div className="mt-1 pt-1 border-t border-neutral-11 px-3 pb-0.5 font-mono text-xxs uppercase tracking-wider text-neutral-7">
                                {t('pages.home.batch.runPlaybook')}
                              </div>
                              {batchTemplates.length === 0 && (
                                <div className="px-3 py-1.5 text-xs text-neutral-6">
                                  {t('pages.home.batch.noPlaybooks')}
                                </div>
                              )}
                              {batchTemplates.map((workflow) => (
                                <BatchMenuRow
                                  key={workflow.id}
                                  label={workflow.name}
                                  onClick={() => {
                                    setBatchMenuOpen(false)
                                    setBatchConfirm({
                                      kind: 'workflow',
                                      templateId: workflow.id,
                                    })
                                  }}
                                />
                              ))}
                            </>
                          )}
                          {(canBatchShutdown || canBatchDelete) && (
                            <>
                              <div className="mt-1 pt-1 border-t border-neutral-11 px-3 pb-0.5 font-mono text-xxs uppercase tracking-wider text-red-6/80">
                                {t('pages.home.batch.danger')}
                              </div>
                              {canBatchShutdown && (
                                <BatchMenuRow
                                  label={t('pages.home.batch.shutdown')}
                                  accent="red"
                                  onClick={() => {
                                    setBatchMenuOpen(false)
                                    setBatchConfirm({ kind: 'shutdown' })
                                  }}
                                />
                              )}
                              {canBatchDelete && (
                                <BatchMenuRow
                                  label={t('pages.home.batch.delete')}
                                  accent="red"
                                  onClick={() => {
                                    setBatchMenuOpen(false)
                                    setBatchConfirm({ kind: 'delete' })
                                  }}
                                />
                              )}
                            </>
                          )}
                        </div>
                      </>
                    )}
                  </div>
                )}
              </>
            )}
          </div>
          <div className="flex items-center gap-2">
            {/* Gated: node.create is admin-only, and this is the landing page's
              primary button. A viewer used to be able to walk the whole Add Node
              flow and receive a 403 at the end with nothing on screen. */}
            {!isMultiselectMode && check(Actions.NodeCreate) && (
              <Button size="md" onClick={handleAddNode}>
                {t('pages.home.addNode')}
              </Button>
            )}
          </div>
        </div>
        {batchDeleteError && (
          <div className="mb-4 bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
            {batchDeleteError}
          </div>
        )}

        {/* Filters */}
        <div className="flex items-center gap-3 mb-4">
          <input
            type="search"
            value={searchQuery}
            onChange={(e) => {
              setSearchQuery(e.target.value)
              setPage(0)
            }}
            placeholder={t('pages.home.filters.searchPlaceholder')}
            aria-label={t('pages.home.filters.searchAria')}
            className="flex-1 max-w-md bg-neutral-13 rounded px-3 py-1.5 text-sm text-neutral-2 placeholder:text-neutral-7 outline-none focus:ring-1 focus:ring-lime-6/50"
          />
          <select
            value={statusFilter}
            onChange={(e) => {
              if (isNodeStatusFilter(e.target.value)) {
                setStatusFilter(e.target.value)
                setPage(0)
              }
            }}
            aria-label={t('pages.home.filters.statusAria')}
            className="bg-neutral-13 rounded px-2.5 py-1.5 text-sm text-neutral-2 outline-none"
          >
            {NODE_STATUS_FILTERS.filter(
              // The deleted option exists only for a viewer who can see those
              // rows; offering it to everyone else yields a permanently empty
              // list with no explanation.
              (f) => f !== 'deleted' || canBatchDelete,
            ).map((f) => (
              <option key={f} value={f}>
                {t(`pages.home.filters.status.${f}`)}
              </option>
            ))}
          </select>
          <select
            value={boardFilter}
            onChange={(e) => {
              setBoardFilter(e.target.value)
              setPage(0)
            }}
            aria-label={t('pages.home.filters.boardAria')}
            className="bg-neutral-13 rounded px-2.5 py-1.5 text-sm text-neutral-2 outline-none"
          >
            <option value="all">{t('pages.home.filters.allBoards')}</option>
            {boardOptions.map((b) => (
              <option key={b} value={b}>
                {b}
              </option>
            ))}
          </select>
          <select
            value={ramFilter}
            onChange={(e) => {
              setRamFilter(e.target.value as 'all' | '4' | '8' | '16')
              setPage(0)
            }}
            aria-label={t('pages.home.filters.ramAria')}
            className="bg-neutral-13 rounded px-2.5 py-1.5 text-sm text-neutral-2 outline-none"
          >
            <option value="all">{t('pages.home.filters.anyRam')}</option>
            <option value="4">
              {t('pages.home.filters.atLeastGb', { gb: 4 })}
            </option>
            <option value="8">
              {t('pages.home.filters.atLeastGb', { gb: 8 })}
            </option>
            <option value="16">
              {t('pages.home.filters.atLeastGb', { gb: 16 })}
            </option>
          </select>
          <select
            value={diskFilter}
            onChange={(e) => {
              setDiskFilter(
                e.target.value as 'all' | '128' | '256' | '512' | '1000',
              )
              setPage(0)
            }}
            aria-label={t('pages.home.filters.diskAria')}
            className="bg-neutral-13 rounded px-2.5 py-1.5 text-sm text-neutral-2 outline-none"
          >
            <option value="all">{t('pages.home.filters.anyDisk')}</option>
            <option value="128">
              {t('pages.home.filters.atLeastGb', { gb: 128 })}
            </option>
            <option value="256">
              {t('pages.home.filters.atLeastGb', { gb: 256 })}
            </option>
            <option value="512">
              {t('pages.home.filters.atLeastGb', { gb: 512 })}
            </option>
            <option value="1000">{t('pages.home.filters.atLeast1Tb')}</option>
          </select>
        </div>

        {/* The deleted view is the one place the explanation of what these
          rows are fits: in the mixed list each row carries its own outlook
          sentence, and a paragraph above a fleet of live nodes would be about
          the rows at its very bottom. */}
        {statusFilter === 'deleted' && (
          <p className="mb-3 text-xs text-neutral-6">
            {t('pages.home.deletedView.description')}
          </p>
        )}

        <NodeList
          {...locator.nodes()}
          nodes={pagedNodes}
          deletedNodes={pagedDeleted}
          selectedDeletedNodeId={selectedDeletedNodeId}
          onDeletedNodeClick={handleOpenDeletedNode}
          emptyMessage={
            filtersActive ? t('entities.nodeList.list.noMatch') : undefined
          }
          onNodeClick={handleOpenNode}
          selectedNodeId={selectedNodeId}
          isMultiselectMode={isMultiselectMode}
          selectedNodeIds={selectedNodeIds}
          onSelectNode={handleSelectNode}
          onSelectAll={handleSelectAll}
          allSelected={allSelected}
          dragEnabled={nodesDraggable}
          sort={sort}
          onSortChange={handleSortChange}
        />

        {/* Pager */}
        {totalRows > 0 && (
          <div className="flex items-center justify-between mt-4 text-sm text-neutral-5">
            <div className="flex items-center gap-2">
              <span className="text-xs uppercase tracking-wide text-neutral-7">
                {t('pages.secrets.perPage')}
              </span>
              <select
                value={pageSize}
                onChange={(e) => handlePageSizeChange(Number(e.target.value))}
                aria-label={t('pages.home.pager.perPageAria')}
                className="bg-neutral-13 rounded px-2 py-1 text-sm text-neutral-2 outline-none"
              >
                {[10, 20, 50, 100].map((size) => (
                  <option key={size} value={size}>
                    {size}
                  </option>
                ))}
              </select>
            </div>
            <div className="flex items-center gap-3">
              <span>
                {t('pages.secrets.range', {
                  from: currentPage * pageSize + 1,
                  to: Math.min((currentPage + 1) * pageSize, totalRows),
                  total: totalRows,
                })}
              </span>
              <button
                type="button"
                onClick={() => setPage(Math.max(0, currentPage - 1))}
                disabled={currentPage === 0}
                aria-label={t('pages.secrets.prevPage')}
                className="px-2 py-0.5 rounded text-neutral-4 hover:text-neutral-2 hover:bg-neutral-13 disabled:opacity-40 disabled:hover:bg-transparent"
              >
                ‹
              </button>
              <button
                type="button"
                onClick={() =>
                  setPage(Math.min(pageCount - 1, currentPage + 1))
                }
                disabled={currentPage >= pageCount - 1}
                aria-label={t('pages.secrets.nextPage')}
                className="px-2 py-0.5 rounded text-neutral-4 hover:text-neutral-2 hover:bg-neutral-13 disabled:opacity-40 disabled:hover:bg-transparent"
              >
                ›
              </button>
            </div>
          </div>
        )}
      </div>
      <ConfirmDialog
        open={!!batchConfirm}
        title={batchConfirmText.title}
        message={batchConfirmText.message}
        confirmText={
          batchConfirm?.kind === 'delete'
            ? t('pages.home.batch.delete')
            : t('pages.home.batch.confirm.confirm')
        }
        destructive={
          batchConfirm?.kind === 'delete' || batchConfirm?.kind === 'shutdown'
        }
        onConfirm={handleBatchConfirm}
        onCancel={() => setBatchConfirm(null)}
      />
    </div>
  )
}
