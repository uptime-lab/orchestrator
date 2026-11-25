import { Dialog, DialogPanel, DialogTitle } from '@headlessui/react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { UtilitiesTab } from '@/routes/_mainLayout/utilities'

import { OU_FILTER_ALL } from '@/widgets/ouRail'
import { OURailAside } from '@/widgets/ouRail/OURailAside.tsx'
import { SidebarEnum } from '@/widgets/sidebars/types.ts'

import { useNodes } from '@/entities/node/hooks.ts'
import { useOUs } from '@/entities/ou'
import { Actions, useCan, useCanAnyScope } from '@/entities/permissions/hooks'
import {
  applyUtilityAssignment,
  assignmentOUIDs,
  canManageAssignment,
  createUtilityAssignment,
  deleteUtilityAssignment,
  getUtilityAssignment,
  isAssignmentConflictError,
  isDuplicateAssignmentError,
  updateUtilityAssignment,
  useInvalidateUtilityLists,
  useUtilityAssignments,
  useUtilityCatalog,
  useUtilityOUCounts,
  UTILITY_ASSIGNMENT_DND_TYPE,
  UTILITY_DND_TYPE,
  utilityKeys,
  type CreateUtilityAssignmentPayload,
  type UpdateUtilityAssignmentPayload,
  type UtilityApplyResult,
  type UtilityAssignment,
  type UtilityDefinition,
} from '@/entities/utility'

import Badge from '@/shared/ui/badge'
import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { Column, DataTable } from '@/shared/ui/dataTable'
import { InfoHint } from '@/shared/ui/infoHint'
import { showError, showSuccess } from '@/shared/ui/toast'

const routeApi = getRouteApi('/_mainLayout/utilities')

// Rail selection sentinels (the credentials-page shape): "All nodes" selects
// the fleet-wide bucket, "Unassigned" the applies-nowhere bucket, "Assigned to
// nodes" the node-bound bucket (not a drop target — a drop can't pick which
// node; per-node assignment lives in the node sidebar's Utilities tab).
const ACCOUNT_WIDE_FILTER = '__account_wide__'
const UNASSIGNED_FILTER = '__unassigned__'
const NODE_FILTER = '__node__'

// How many OU-name chips the Scope column shows before folding into "+N".
const SCOPE_CHIPS_SHOWN = 2

const formatDate = (value: string | number): string => {
  const date =
    typeof value === 'number' ? new Date(value * 1000) : new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : date.toLocaleString()
}

// Short "k: v, k: v (+N)" summary of an assignment's config for the table.
const configSummary = (config?: Record<string, unknown>): string => {
  const entries = Object.entries(config ?? {})
  if (entries.length === 0) return ''
  // Values are cut short so a credential pasted as a plain param (a VPN
  // pre-auth key, say) is not readable off a list view; the full config is
  // one Edit away.
  const shown = entries
    .slice(0, 2)
    .map(([k, v]) => {
      const value = String(v)
      return `${k}: ${value.length > 12 ? `${value.slice(0, 12)}…` : value}`
    })
    .join(', ')
  return entries.length > 2 ? `${shown} (+${entries.length - 2})` : shown
}

// The outcome of one explicit Apply, kept for the results dialog: skipped
// reasons must be shown verbatim and queued must be explained, so a toast is
// too small a surface.
type ApplyOutcome = {
  utilityName: string
  results: UtilityApplyResult[]
}

export const UtilitiesPage = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = routeApi.useSearch()
  const queryClient = useQueryClient()
  const check = useCan()
  const checkAnyScope = useCanAnyScope()
  // The page offers management to anyone who manages utilities somewhere;
  // each assignment is then gated by the server's own rule for it (see
  // canManageAssignment), so a member who manages one unit can manage that
  // unit's assignments.
  const canManage = checkAnyScope(Actions.UtilityManage)
  const tab = search.tab

  const { ous } = useOUs()
  const { nodes } = useNodes()
  const canManageRow = (a: UtilityAssignment) =>
    canManageAssignment(
      (ou) => check(Actions.UtilityManage, ou ?? undefined),
      a,
      (nodeId) => nodes.find((n) => n.id === nodeId)?.ou_id,
    )
  const { data: catalog } = useUtilityCatalog()
  const { data: assignments } = useUtilityAssignments()
  const { data: ouCounts } = useUtilityOUCounts()
  const invalidateLists = useInvalidateUtilityLists()

  const [pickerOpen, setPickerOpen] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<UtilityAssignment | null>(
    null,
  )
  const [applyOutcome, setApplyOutcome] = useState<ApplyOutcome | null>(null)

  const definitionsById = useMemo(() => {
    const out: Record<string, UtilityDefinition> = {}
    for (const def of catalog ?? []) out[def.id] = def
    return out
  }, [catalog])

  const utilityName = useCallback(
    (utilityId: string) => definitionsById[utilityId]?.name ?? utilityId,
    [definitionsById],
  )

  const setTab = useCallback(
    (next: UtilitiesTab) => {
      navigate({
        to: '.',
        search: (prev) => ({ ...prev, tab: next, sidebar: undefined }),
        replace: true,
      })
    },
    [navigate],
  )

  const selectedOU =
    search.ou &&
    search.ou !== OU_FILTER_ALL &&
    search.ou !== ACCOUNT_WIDE_FILTER &&
    search.ou !== UNASSIGNED_FILTER &&
    search.ou !== NODE_FILTER
      ? ous.find((ou) => ou.id === search.ou)
      : undefined
  // A new assignment lands on the rail's selected unit, or unassigned
  // (account level) without one; the right is needed there.
  const canCreateHere = selectedOU
    ? check(Actions.UtilityManage, selectedOU.id)
    : check(Actions.UtilityManage)

  const scopeLabel =
    selectedOU?.name ??
    (search.ou === ACCOUNT_WIDE_FILTER
      ? t('pages.utilities.accountWide')
      : search.ou === UNASSIGNED_FILTER
        ? t('pages.utilities.unassigned')
        : search.ou === NODE_FILTER
          ? t('pages.utilities.rail.nodes')
          : t('pages.utilities.allScopes'))

  const visibleAssignments = useMemo(() => {
    const all = assignments ?? []
    if (selectedOU) {
      // Set-aware: an assignment matches the unit when ANY of its OUs is it.
      return all.filter((a) => assignmentOUIDs(a).includes(selectedOU.id))
    }
    switch (search.ou) {
      case ACCOUNT_WIDE_FILTER:
        return all.filter((a) => a.scope === 'account')
      case UNASSIGNED_FILTER:
        return all.filter((a) => a.scope === 'unassigned')
      case NODE_FILTER:
        return all.filter((a) => a.scope === 'node')
      default:
        return all
    }
  }, [assignments, selectedOU, search.ou])

  const openAssignment = useCallback(
    (assignmentId: string) => {
      navigate({
        to: '.',
        search: (prev) => ({
          ...prev,
          sidebar: SidebarEnum.UtilityAssignmentDetails,
          assignmentId,
        }),
      })
    },
    [navigate],
  )

  // Creation (drop on the rail, or the catalog's Assign action) always ends in
  // the config sidebar: a bare drop must not leave an invisible
  // half-configured state.
  const create = useMutation({
    mutationFn: (payload: CreateUtilityAssignmentPayload) =>
      createUtilityAssignment(payload),
    onSuccess: (created) => {
      invalidateLists()
      setPickerOpen(false)
      showSuccess(t('pages.utilities.toasts.created'))
      openAssignment(created.id)
    },
    onError: (e: unknown) => {
      if (isDuplicateAssignmentError(e)) {
        showError(t('pages.utilities.toasts.duplicate'))
      } else if (isAssignmentConflictError(e)) {
        // Another assignment of the utility covers that unit — the server's
        // message names the conflict; show it verbatim, never retry.
        showError(t('pages.utilities.toasts.scopeConflict'), e)
      } else {
        showError(t('pages.utilities.toasts.createFailed'), e)
      }
    },
  })

  // Dragging an ASSIGNMENT row onto the rail re-targets it (the workflows
  // drag-to-scope precedent): a unit is TOGGLED in the assignment's OU set,
  // because otherwise the rail is a one-way ratchet — every drop widens the
  // scope and nothing narrows it.
  const retarget = useMutation({
    mutationFn: async ({
      assignmentId,
      payload,
    }: {
      assignmentId: string
      payload:
        | UpdateUtilityAssignmentPayload
        | (() => Promise<UpdateUtilityAssignmentPayload>)
    }) =>
      updateUtilityAssignment(
        assignmentId,
        typeof payload === 'function' ? await payload() : payload,
      ),
    onSuccess: (updated) => {
      queryClient.setQueryData(utilityKeys.assignment(updated.id), updated)
      invalidateLists()
    },
    onError: (e: unknown) => {
      if (isAssignmentConflictError(e)) {
        showError(t('pages.utilities.toasts.scopeConflict'), e)
      } else {
        showError(t('pages.utilities.toasts.updateFailed'), e)
      }
    },
  })

  const handleDropAssignment = useCallback(
    (assignmentId: string, target: string | null) => {
      const assignment = (assignments ?? []).find((a) => a.id === assignmentId)
      if (!assignment) return
      if (target === ACCOUNT_WIDE_FILTER) {
        if (assignment.scope === 'account') return
        retarget.mutate({
          assignmentId,
          payload: { retarget: true, account_wide: true },
        })
        return
      }
      if (target === UNASSIGNED_FILTER || target === null) {
        if (assignment.scope === 'unassigned') return
        retarget.mutate({ assignmentId, payload: { retarget: true } })
        return
      }
      // The unit set is toggled against the assignment as the server holds
      // it now: the route replaces the whole set, and this list may be older
      // than a change made elsewhere.
      retarget.mutate({
        assignmentId,
        payload: async () => {
          const current = assignmentOUIDs(
            await getUtilityAssignment(assignmentId),
          )
          return {
            retarget: true,
            ou_ids: current.includes(target)
              ? current.filter((id) => id !== target)
              : [...current, target],
          }
        },
      })
    },
    [assignments, retarget],
  )

  const toggleEnabled = useMutation({
    mutationFn: (assignment: UtilityAssignment) =>
      updateUtilityAssignment(assignment.id, { enabled: !assignment.enabled }),
    onSuccess: () => invalidateLists(),
    onError: (e: unknown) =>
      showError(t('pages.utilities.toasts.updateFailed'), e),
  })

  const remove = useMutation({
    mutationFn: (assignmentId: string) => deleteUtilityAssignment(assignmentId),
    onSuccess: () => {
      invalidateLists()
      showSuccess(t('pages.utilities.toasts.deleted'))
    },
    onError: (e: unknown) =>
      showError(t('pages.utilities.toasts.deleteFailed'), e),
    onSettled: () => setPendingDelete(null),
  })

  const apply = useMutation({
    mutationFn: (assignment: UtilityAssignment) =>
      applyUtilityAssignment(assignment.id),
    onSuccess: (results, assignment) => {
      setApplyOutcome({
        utilityName: utilityName(assignment.utility_id),
        results,
      })
    },
    onError: (e: unknown) =>
      showError(t('pages.utilities.toasts.applyFailed'), e),
  })

  const ouName = useCallback(
    (id: string) => ous.find((ou) => ou.id === id)?.name ?? id,
    [ous],
  )

  const assignmentScopeLabel = useCallback(
    (a: UtilityAssignment): string => {
      switch (a.scope) {
        case 'account':
          return t('pages.utilities.accountWide')
        case 'ou':
          return assignmentOUIDs(a).map(ouName).join(', ')
        case 'node':
          return (
            nodes.find((n) => n.id === a.node_id)?.name ??
            t('pages.utilities.table.nodeScope')
          )
        default:
          return t('pages.utilities.unassigned')
      }
    },
    [t, ouName, nodes],
  )

  const columns: Column<UtilityAssignment>[] = [
    {
      key: 'utility',
      header: t('pages.utilities.table.utility'),
      alwaysVisible: true,
      sortValue: (a) => utilityName(a.utility_id),
      render: (a) => (
        <span className="text-neutral-2">{utilityName(a.utility_id)}</span>
      ),
    },
    {
      key: 'scope',
      header: t('pages.utilities.table.scope'),
      sortValue: (a) => assignmentScopeLabel(a),
      render: (a) => {
        if (a.scope === 'unassigned') {
          return (
            <Badge variant="outline" color="neutral">
              {t('pages.utilities.unassigned')}
            </Badge>
          )
        }
        if (a.scope !== 'ou') {
          return (
            <span className="text-neutral-4">{assignmentScopeLabel(a)}</span>
          )
        }
        // The OU SET as chips; folded into "+N" past a couple so a wide scope
        // does not blow up the row (the workflow scope-column precedent).
        const names = assignmentOUIDs(a).map(ouName)
        const shown = names.slice(0, SCOPE_CHIPS_SHOWN)
        const rest = names.length - shown.length
        return (
          <span className="flex flex-wrap items-center gap-1">
            {shown.map((name) => (
              <span
                key={name}
                className="rounded border border-neutral-11 px-1.5 py-0.5 text-xs text-neutral-3"
              >
                {name}
              </span>
            ))}
            {rest > 0 && (
              <span
                title={names.slice(SCOPE_CHIPS_SHOWN).join(', ')}
                className="rounded border border-neutral-11 px-1.5 py-0.5 text-xs text-neutral-5"
              >
                {t('pages.utilities.table.moreOUs', { count: rest })}
              </span>
            )}
          </span>
        )
      },
    },
    {
      key: 'enabled',
      header: t('pages.utilities.table.enabled'),
      sortValue: (a) => (a.enabled ? 1 : 0),
      render: (a) => (
        <input
          type="checkbox"
          checked={a.enabled}
          disabled={!canManageRow(a) || toggleEnabled.isPending}
          aria-label={t('pages.utilities.table.enabled')}
          onClick={(e) => e.stopPropagation()}
          onChange={() => toggleEnabled.mutate(a)}
        />
      ),
    },
    {
      key: 'config',
      header: t('pages.utilities.table.config'),
      className: 'max-w-[280px] truncate',
      sortValue: (a) => configSummary(a.config),
      render: (a) => {
        const summary = configSummary(a.config)
        return summary ? (
          <span className="font-mono text-xs text-neutral-4">{summary}</span>
        ) : (
          <span className="text-neutral-7">—</span>
        )
      },
    },
    {
      key: 'updated',
      header: t('pages.utilities.table.updated'),
      sortValue: (a) => String(a.updated_at),
      render: (a) => (
        <span className="whitespace-nowrap text-neutral-5">
          {formatDate(a.updated_at)}
        </span>
      ),
    },
    {
      key: 'created',
      header: t('pages.utilities.table.created'),
      defaultHidden: true,
      sortValue: (a) => String(a.created_at),
      render: (a) => (
        <span className="whitespace-nowrap text-neutral-5">
          {formatDate(a.created_at)}
        </span>
      ),
    },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: '',
            alwaysVisible: true,
            render: (a: UtilityAssignment) =>
              canManageRow(a) ? (
                <span
                  className="flex items-center justify-end gap-2"
                  onClick={(e) => e.stopPropagation()}
                >
                  <Button
                    size="sm"
                    kind="tertiary"
                    onClick={() => openAssignment(a.id)}
                  >
                    {t('pages.utilities.actions.edit')}
                  </Button>
                  <Button
                    size="sm"
                    kind="secondary"
                    disabled={apply.isPending || !a.enabled}
                    title={
                      a.enabled
                        ? t('pages.utilities.sidebar.applyHint')
                        : t('pages.utilities.sidebar.applyDisabledHint')
                    }
                    onClick={() => apply.mutate(a)}
                  >
                    {t('pages.utilities.actions.apply')}
                  </Button>
                  <Button
                    size="sm"
                    kind="secondary"
                    variant="destructive"
                    onClick={() => setPendingDelete(a)}
                  >
                    {t('pages.utilities.actions.delete')}
                  </Button>
                </span>
              ) : null,
          } satisfies Column<UtilityAssignment>,
        ]
      : []),
  ]

  const tabs: { id: UtilitiesTab; label: string }[] = [
    { id: 'catalog', label: t('pages.utilities.tabs.catalog') },
    { id: 'assignments', label: t('pages.utilities.tabs.assignments') },
  ]

  return (
    <div className="h-full w-full flex">
      <UtilitiesFilterRail
        assignments={assignments ?? null}
        ouCounts={ouCounts ?? {}}
        canManage={canManage}
        onDropUtility={(utilityId, payload) =>
          create.mutate({ utility_id: utilityId, ...payload })
        }
        onDropAssignment={handleDropAssignment}
      />

      <div className="flex-1 min-w-0 overflow-y-auto p-4 pt-8 md:p-8">
        <h1 className="text-lg uppercase font-bold mb-6">
          {t('pages.utilities.title')}
        </h1>

        <div className="flex gap-1 border-b border-neutral-12 mb-6">
          {tabs.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => setTab(item.id)}
              className={clsx(
                'font-mono text-sm px-3 py-2 border-b-2 -mb-px transition-colors',
                tab === item.id
                  ? 'text-lime-6 border-lime-6'
                  : 'text-neutral-4 hover:text-neutral-2 border-transparent',
              )}
            >
              {item.label}
            </button>
          ))}
        </div>

        {tab === 'catalog' && (
          <>
            <p className="text-sm text-neutral-5 max-w-3xl mb-6">
              {t('pages.utilities.catalogHint')}
            </p>
            <CatalogTable
              catalog={catalog ?? []}
              canManage={canCreateHere}
              busy={create.isPending}
              onAssign={(def) =>
                create.mutate({
                  utility_id: def.id,
                  ...(selectedOU ? { ou_ids: [selectedOU.id] } : {}),
                })
              }
            />
          </>
        )}

        {tab === 'assignments' && (
          <>
            <div className="flex items-center justify-between gap-4 mb-6">
              <p className="text-sm text-neutral-5 max-w-3xl">
                {t('pages.utilities.hint')}
                <span className="whitespace-nowrap">
                  &nbsp;
                  <InfoHint text={t('pages.utilities.hintTooltip')} />
                </span>
              </p>
              {canCreateHere && (
                <Button size="md" onClick={() => setPickerOpen(true)}>
                  {t('pages.utilities.addUtility')}
                </Button>
              )}
            </div>

            <DataTable
              storageKey="utilities-table"
              columnsLabel={t('pages.utilities.columns')}
              columns={columns}
              rows={visibleAssignments}
              rowKey={(a) => a.id}
              isSelected={(a) =>
                search.sidebar === 'utilityAssignmentDetails' &&
                search.assignmentId === a.id
              }
              onRowClick={(a) => openAssignment(a.id)}
              rowDraggable={canManage ? (a) => canManageRow(a) : undefined}
              onRowDragStart={
                canManage
                  ? (a, e) => {
                      e.dataTransfer.setData(UTILITY_ASSIGNMENT_DND_TYPE, a.id)
                      e.dataTransfer.effectAllowed = 'move'
                    }
                  : undefined
              }
              empty={
                <div>
                  <p className="text-sm text-neutral-6">
                    {(assignments?.length ?? 0) === 0
                      ? t('pages.utilities.table.emptyAll')
                      : t('pages.utilities.table.empty', { scope: scopeLabel })}
                  </p>
                  {canCreateHere && (
                    <Button
                      size="sm"
                      className="mt-4"
                      onClick={() => setPickerOpen(true)}
                    >
                      {t('pages.utilities.addUtility')}
                    </Button>
                  )}
                </div>
              }
            />
          </>
        )}
      </div>

      <AddUtilityDialog
        open={pickerOpen}
        catalog={catalog ?? []}
        preselectedOUName={selectedOU?.name}
        busy={create.isPending}
        onPick={(def) =>
          create.mutate({
            utility_id: def.id,
            ...(selectedOU ? { ou_ids: [selectedOU.id] } : {}),
          })
        }
        onClose={() => setPickerOpen(false)}
      />

      <ConfirmDialog
        open={pendingDelete !== null}
        title={t('pages.utilities.deleteDialog.title')}
        message={t('pages.utilities.deleteDialog.message', {
          utility: pendingDelete ? utilityName(pendingDelete.utility_id) : '',
          scope: pendingDelete ? assignmentScopeLabel(pendingDelete) : '',
        })}
        confirmText={t('pages.utilities.deleteDialog.confirm')}
        destructive
        busy={remove.isPending}
        onConfirm={() => {
          if (pendingDelete) remove.mutate(pendingDelete.id)
        }}
        onCancel={() => setPendingDelete(null)}
      />

      <ApplyResultsDialog
        outcome={applyOutcome}
        onClose={() => setApplyOutcome(null)}
      />
    </div>
  )
}

type CatalogTableProps = {
  catalog: UtilityDefinition[]
  canManage: boolean
  busy: boolean
  // The picker-less create flow: create the assignment (unassigned, or scoped
  // to the rail's selected unit) and open the config sidebar.
  onAssign: (def: UtilityDefinition) => void
}

// The Catalog tab: what each utility IS. The full description is the point —
// the operator reads what a utility does right here, then drags the row onto a
// unit in the rail (creates an assignment there) or hits Assign.
const CatalogTable = ({
  catalog,
  canManage,
  busy,
  onAssign,
}: CatalogTableProps) => {
  const { t } = useTranslation()

  const columns: Column<UtilityDefinition>[] = [
    {
      key: 'name',
      header: t('pages.utilities.catalogTable.name'),
      alwaysVisible: true,
      className: 'whitespace-nowrap',
      sortValue: (def) => def.name,
      render: (def) => <span className="text-neutral-2">{def.name}</span>,
    },
    {
      key: 'description',
      header: t('pages.utilities.catalogTable.description'),
      sortValue: (def) => def.description,
      // Full text, wrapped — never truncated: reading it is what the tab is for.
      className: 'min-w-[240px]',
      render: (def) => (
        <span className="block whitespace-normal text-neutral-4">
          {def.description}
          {def.details && (
            <span className="whitespace-nowrap">
              &nbsp;
              <InfoHint text={def.details} />
            </span>
          )}
        </span>
      ),
    },
    {
      key: 'class',
      header: t('pages.utilities.catalogTable.class'),
      sortValue: (def) => def.class,
      render: (def) => (
        <span className="flex flex-wrap items-center gap-1.5">
          <Badge variant="outline" color="blue">
            {t('pages.utilities.classes.reconciled')}
          </Badge>
          {def.requires_reboot && (
            <Badge variant="outline" color="yellow">
              {t('pages.utilities.sidebar.requiresReboot')}
            </Badge>
          )}
        </span>
      ),
    },
    {
      key: 'depends',
      header: t('pages.utilities.catalogTable.dependsOn'),
      sortValue: (def) => (def.depends_on ?? []).join(', '),
      render: (def) =>
        (def.depends_on?.length ?? 0) > 0 ? (
          <span className="text-neutral-4">{def.depends_on?.join(', ')}</span>
        ) : (
          <span className="text-neutral-7">—</span>
        ),
    },
    {
      key: 'params',
      header: t('pages.utilities.catalogTable.params'),
      sortValue: (def) => def.params.length,
      render: (def) => {
        const names = def.params.map((p) => p.label || p.name)
        if (names.length === 0) {
          return <span className="text-neutral-7">—</span>
        }
        const shown = names.slice(0, 3).join(', ')
        return (
          <span className="text-xs text-neutral-4" title={names.join(', ')}>
            {names.length > 3 ? `${shown} (+${names.length - 3})` : shown}
          </span>
        )
      },
    },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: '',
            alwaysVisible: true,
            render: (def: UtilityDefinition) => (
              <span
                className="flex justify-end"
                onClick={(e) => e.stopPropagation()}
              >
                <Button size="sm" disabled={busy} onClick={() => onAssign(def)}>
                  {t('pages.utilities.catalogTable.assign')}
                </Button>
              </span>
            ),
          } satisfies Column<UtilityDefinition>,
        ]
      : []),
  ]

  return (
    <DataTable
      storageKey="utilities-catalog-table"
      columnsLabel={t('pages.utilities.columns')}
      columns={columns}
      rows={catalog}
      rowKey={(def) => def.id}
      rowDraggable={canManage ? () => true : undefined}
      onRowDragStart={
        canManage
          ? (def, e) => {
              e.dataTransfer.setData(UTILITY_DND_TYPE, def.id)
              e.dataTransfer.effectAllowed = 'copy'
            }
          : undefined
      }
      empty={
        <p className="text-sm text-neutral-6">
          {t('pages.utilities.picker.emptyCatalog')}
        </p>
      }
    />
  )
}

type RailProps = {
  assignments: UtilityAssignment[] | null
  ouCounts: Record<string, number>
  canManage: boolean
  // A catalog row dropped on the rail: the payload carries the target
  // (account_wide / ou_ids / neither = unassigned).
  onDropUtility: (
    utilityId: string,
    payload: Pick<CreateUtilityAssignmentPayload, 'account_wide' | 'ou_ids'>,
  ) => void
  // An assignment row dropped on the rail: toggles the unit in the OU set, or
  // re-targets to account-wide/unassigned via the pseudo rows.
  onDropAssignment: (assignmentId: string, target: string | null) => void
}

const UtilitiesFilterRail = ({
  assignments,
  ouCounts,
  canManage,
  onDropUtility,
  onDropAssignment,
}: RailProps) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = routeApi.useSearch()

  const selectedOU = search.ou ?? OU_FILTER_ALL

  const totals = useMemo(() => {
    const all = assignments ?? []
    return {
      all: all.length,
      unassigned: all.filter((a) => a.scope === 'unassigned').length,
      accountWide: all.filter((a) => a.scope === 'account').length,
      nodes: all.filter((a) => a.scope === 'node').length,
    }
  }, [assignments])

  const setFilterOU = (value: string) =>
    navigate({
      to: '.',
      search: (prev) => ({
        ...prev,
        ou: value === OU_FILTER_ALL ? undefined : value,
      }),
      replace: true,
    })

  const handleDrop = (itemId: string, target: string | null, type: string) => {
    if (type === UTILITY_ASSIGNMENT_DND_TYPE) {
      onDropAssignment(itemId, target)
      return
    }
    if (type !== UTILITY_DND_TYPE) return
    const accountWide = target === ACCOUNT_WIDE_FILTER
    const unassign = target === UNASSIGNED_FILTER || target === null
    onDropUtility(itemId, {
      ...(accountWide ? { account_wide: true } : {}),
      ...(!accountWide && !unassign && target ? { ou_ids: [target] } : {}),
    })
  }

  const pseudoRows = [
    {
      value: OU_FILTER_ALL,
      label: t('pages.utilities.rail.all'),
      count: totals.all,
    },
    {
      value: UNASSIGNED_FILTER,
      label: t('pages.utilities.rail.unassigned'),
      count: totals.unassigned,
      dropTarget: UNASSIGNED_FILTER,
    },
    {
      value: ACCOUNT_WIDE_FILTER,
      label: t('pages.utilities.rail.accountWide'),
      count: totals.accountWide,
      dropTarget: ACCOUNT_WIDE_FILTER,
    },
    // Node-bound assignments. Not a drop target (a drop can't pick which
    // node); per-node assignment lives in the node sidebar's Utilities tab.
    {
      value: NODE_FILTER,
      label: t('pages.utilities.rail.nodes'),
      count: totals.nodes,
    },
  ]

  return (
    <>
      <OURailAside
        storageKey="utilities-ou-panel-collapsed"
        counts={ouCounts}
        selected={selectedOU}
        onSelect={setFilterOU}
        pseudoRows={pseudoRows}
        dropTypes={
          canManage ? [UTILITY_DND_TYPE, UTILITY_ASSIGNMENT_DND_TYPE] : []
        }
        onDropItem={handleDrop}
        dropHint={t('pages.utilities.rail.dropHint')}
        // Accept drops without entering OU edit mode — dragging a catalog or
        // assignment row onto a unit is the primary way to (re)assign here.
        alwaysAcceptDrops={canManage}
      />
    </>
  )
}

type AddUtilityDialogProps = {
  open: boolean
  catalog: UtilityDefinition[]
  preselectedOUName?: string
  busy: boolean
  onPick: (def: UtilityDefinition) => void
  onClose: () => void
}

// Catalog picker behind the "Assign utility" button on the Assignments tab —
// assign without switching to the Catalog tab. Adding creates the assignment
// (unassigned, or scoped to the rail's selected unit) and the page opens the
// config sidebar from the create mutation's success.
const AddUtilityDialog = ({
  open,
  catalog,
  preselectedOUName,
  busy,
  onPick,
  onClose,
}: AddUtilityDialogProps) => {
  const { t } = useTranslation()
  return (
    <Dialog open={open} onClose={onClose} className="relative z-50">
      <div className="fixed inset-0 bg-neutral-15/80" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-lg bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase text-neutral-1">
            {t('pages.utilities.picker.title')}
          </DialogTitle>
          <p className="text-sm text-neutral-4">
            {t('pages.utilities.picker.body', {
              scope: preselectedOUName
                ? t('pages.utilities.picker.bodyOU', { ou: preselectedOUName })
                : t('pages.utilities.picker.bodyUnassigned'),
            })}
          </p>
          <div className="flex flex-col gap-2 max-h-80 overflow-y-auto">
            {catalog.length === 0 && (
              <div className="text-sm text-neutral-6">
                {t('pages.utilities.picker.emptyCatalog')}
              </div>
            )}
            {catalog.map((def) => (
              <div
                key={def.id}
                className="flex items-start justify-between gap-3 rounded border border-neutral-12 p-3"
              >
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="text-sm text-neutral-2">{def.name}</span>
                    <Badge variant="outline" color="blue">
                      {t('pages.utilities.classes.reconciled')}
                    </Badge>
                  </div>
                  <p className="mt-1 text-xs text-neutral-5">
                    {def.description}
                    {def.details && (
                      <span className="whitespace-nowrap">
                        &nbsp;
                        <InfoHint text={def.details} />
                      </span>
                    )}
                  </p>
                  {def.requires_reboot && (
                    <p className="mt-1 text-xxs text-yellow-6">
                      {t('pages.utilities.picker.requiresReboot')}
                    </p>
                  )}
                  {(def.depends_on?.length ?? 0) > 0 && (
                    <p className="mt-1 text-xxs text-neutral-6">
                      {t('pages.utilities.picker.dependsOn', {
                        deps: def.depends_on?.join(', '),
                      })}
                    </p>
                  )}
                </div>
                <Button
                  size="sm"
                  disabled={busy}
                  onClick={() => onPick(def)}
                  className="flex-shrink-0"
                >
                  {t('pages.utilities.picker.add')}
                </Button>
              </div>
            ))}
          </div>
          <div className="flex justify-end">
            <Button size="md" kind="secondary" onClick={onClose}>
              {t('pages.utilities.picker.cancel')}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}

type ApplyResultsDialogProps = {
  outcome: ApplyOutcome | null
  onClose: () => void
}

// Per-node results of an explicit Apply. Queued and skipped are the states
// worth spelling out: queued = registered and delivered when the node
// reconnects on its managed OS; skipped shows the platform's reason verbatim.
const ApplyResultsDialog = ({ outcome, onClose }: ApplyResultsDialogProps) => {
  const { t } = useTranslation()
  return (
    <Dialog open={outcome !== null} onClose={onClose} className="relative z-50">
      <div className="fixed inset-0 bg-neutral-15/80" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-md bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase text-neutral-1">
            {t('pages.utilities.applyResults.titleFor', {
              utility: outcome?.utilityName ?? '',
            })}
          </DialogTitle>
          <div className="flex flex-col gap-2 max-h-80 overflow-y-auto">
            {(outcome?.results.length ?? 0) === 0 ? (
              <div className="text-sm text-neutral-6">
                {t('pages.utilities.applyResults.empty')}
              </div>
            ) : (
              outcome?.results.map((r) => (
                <div
                  key={r.node_id}
                  className="flex items-start justify-between gap-3 text-sm"
                >
                  <span className="text-neutral-2 truncate">{r.node_name}</span>
                  <span
                    className={
                      r.skipped
                        ? 'text-yellow-6 text-right'
                        : r.queued
                          ? 'text-neutral-4 text-right'
                          : 'text-lime-6 text-right'
                    }
                  >
                    {r.skipped
                      ? t('pages.utilities.applyResults.skipped', {
                          reason: r.reason ?? '',
                        })
                      : r.queued
                        ? t('pages.utilities.applyResults.queued')
                        : t('pages.utilities.applyResults.dispatched')}
                  </span>
                </div>
              ))
            )}
          </div>
          <div className="flex justify-end">
            <Button size="md" onClick={onClose}>
              {t('pages.utilities.applyResults.close')}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}
