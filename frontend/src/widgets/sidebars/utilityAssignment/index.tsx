import { useMutation, useQueryClient } from '@tanstack/react-query'
import { FC, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useNodes } from '@/entities/node/hooks.ts'
import { useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import {
  applyUtilityAssignment,
  assignmentOUIDs,
  canManageAssignment,
  getUtilityAssignment,
  isAssignmentConflictError,
  updateUtilityAssignment,
  useInvalidateUtilityLists,
  useUtilityAssignment,
  useUtilityCatalog,
  utilityKeys,
  type UtilityApplyResult,
  type UtilityAssignment,
} from '@/entities/utility'

import Badge from '@/shared/ui/badge'
import { Button } from '@/shared/ui/button/button.tsx'
import { showError, showSuccess } from '@/shared/ui/toast'

import { SidebarCloseButton } from '../closeButton'
import { SidebarProps } from '../types'
import { UtilityParamField } from './paramForm'

// Config panel for one utility assignment: the utility's params rendered as a
// generated form (saved via PUT), the enabled flag, and an explicit Apply.
// The OU SET is edited here as chips + an add-unit select — each op is its own
// immediate PUT {retarget:true} computed from the freshly LOADED assignment
// (the accessUserDetails shape), never from form state, so a stale form can
// never silently revert a target another operator just set. Account-wide and
// node scopes stay read-only labels; re-scoping across kinds is dragging the
// row onto the rail.
export const SidebarUtilityAssignment: FC<
  SidebarProps & { assignmentId: string }
> = ({ assignmentId, onClose }) => {
  const { t } = useTranslation()
  const check = useCan()
  const queryClient = useQueryClient()
  const invalidateLists = useInvalidateUtilityLists()

  const { data: assignment, isLoading } = useUtilityAssignment(assignmentId)
  const { data: catalog } = useUtilityCatalog()
  const { ous } = useOUs()
  const { nodes } = useNodes()
  // The server's rule for this assignment: the right on every unit it
  // targets (or on its node's unit), account-wide otherwise.
  const canManage =
    !!assignment &&
    canManageAssignment(
      (ou) => check(Actions.UtilityManage, ou ?? undefined),
      assignment,
      (nodeId) => nodes.find((n) => n.id === nodeId)?.ou_id,
    )

  const definition = catalog?.find((d) => d.id === assignment?.utility_id)

  // Local form state, seeded once from the loaded assignment. `null` =
  // not seeded yet — the panel is keyed on assignmentId, so switching
  // assignments remounts it and reseeds.
  const [config, setConfig] = useState<Record<string, unknown> | null>(null)
  const [enabled, setEnabled] = useState<boolean>(true)
  // Enabled travels only when this panel changed it, so saving the config
  // does not revert a toggle made from the list in the meantime.
  const [enabledTouched, setEnabledTouched] = useState(false)
  const [dirty, setDirty] = useState(false)
  const [applyResults, setApplyResults] = useState<UtilityApplyResult[] | null>(
    null,
  )

  useEffect(() => {
    if (assignment && config === null) {
      setConfig(assignment.config ?? {})
      setEnabled(assignment.enabled)
    }
  }, [assignment, config])

  const save = useMutation({
    mutationFn: () =>
      updateUtilityAssignment(assignmentId, {
        config: config ?? {},
        enabled: enabledTouched ? enabled : undefined,
      }),
    onSuccess: (updated) => {
      queryClient.setQueryData(utilityKeys.assignment(assignmentId), updated)
      invalidateLists()
      setDirty(false)
      setEnabledTouched(false)
      showSuccess(t('pages.utilities.sidebar.saved'))
    },
    onError: (e: unknown) =>
      showError(t('pages.utilities.toasts.updateFailed'), e),
  })

  const apply = useMutation({
    mutationFn: () => applyUtilityAssignment(assignmentId),
    onSuccess: (results) => setApplyResults(results),
    onError: (e: unknown) =>
      showError(t('pages.utilities.toasts.applyFailed'), e),
  })

  // Re-target: replaces the whole scope with the given OU set. An empty set is
  // "unassigned" — the safe parking state, not a delete.
  // `next` is applied to the assignment's units as the server holds them now,
  // not to this panel's copy: the route replaces the whole set.
  const retarget = useMutation({
    mutationFn: async (next: (current: string[]) => string[]) =>
      updateUtilityAssignment(assignmentId, {
        retarget: true,
        ou_ids: next(assignmentOUIDs(await getUtilityAssignment(assignmentId))),
      }),
    onSuccess: (updated) => {
      queryClient.setQueryData(utilityKeys.assignment(assignmentId), updated)
      invalidateLists()
    },
    onError: (e: unknown) => {
      if (isAssignmentConflictError(e)) {
        // Another assignment of this utility covers that unit — the server's
        // message names the conflict; show it verbatim, never retry.
        showError(t('pages.utilities.toasts.scopeConflict'), e)
      } else {
        showError(t('pages.utilities.toasts.updateFailed'), e)
      }
    },
  })

  const scopeLabel = (a: UtilityAssignment): string => {
    switch (a.scope) {
      case 'account':
        return t('pages.utilities.accountWide')
      case 'node':
        return (
          nodes.find((n) => n.id === a.node_id)?.name ??
          t('pages.utilities.table.nodeScope')
        )
      default:
        return t('pages.utilities.unassigned')
    }
  }

  if (isLoading || !assignment || config === null) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        <div className="flex items-center justify-between">
          <h2 className="text-sm font-bold uppercase text-neutral-1">
            {t('pages.utilities.sidebar.title')}
          </h2>
          <SidebarCloseButton onClose={onClose} />
        </div>
        <div className="text-sm text-neutral-6">
          {isLoading
            ? t('pages.utilities.sidebar.loading')
            : t('pages.utilities.sidebar.notFound')}
        </div>
      </div>
    )
  }

  const params = definition?.params ?? []
  const scopeOUs = assignmentOUIDs(assignment)
  const addableOUs = ous.filter((ou) => !scopeOUs.includes(ou.id))
  const ouSetEditable =
    assignment.scope === 'ou' || assignment.scope === 'unassigned'

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-5">
      <div className="flex items-center justify-between gap-2">
        <h2 className="text-sm font-bold uppercase text-neutral-1 truncate">
          {definition?.name ?? assignment.utility_id}
        </h2>
        <SidebarCloseButton onClose={onClose} />
      </div>

      <div className="flex items-center gap-2">
        <Badge variant="outline" color="blue">
          {t('pages.utilities.classes.reconciled')}
        </Badge>
        {definition?.requires_reboot && (
          <Badge variant="outline" color="yellow">
            {t('pages.utilities.sidebar.requiresReboot')}
          </Badge>
        )}
      </div>

      {definition?.description && (
        <p className="text-sm text-neutral-5">{definition.description}</p>
      )}

      <div className="flex flex-col gap-1 text-sm">
        {ouSetEditable ? (
          // The OU SET as removable chips + an add-unit select. Each op is one
          // immediate PUT {retarget:true, ou_ids} computed from the loaded
          // assignment; removing the last unit parks it as unassigned.
          <div className="flex flex-col gap-2 mb-1">
            <span className="text-neutral-7">
              {t('pages.utilities.sidebar.scope')}
            </span>
            {scopeOUs.length > 0 ? (
              <div className="flex flex-wrap gap-1.5">
                {scopeOUs.map((id) => (
                  <span
                    key={id}
                    className="flex items-center gap-1.5 rounded border border-neutral-11 px-2 py-0.5 text-xs text-neutral-3"
                  >
                    {ous.find((o) => o.id === id)?.name ?? id}
                    {canManage && (
                      <button
                        type="button"
                        disabled={retarget.isPending}
                        aria-label={t('pages.utilities.sidebar.removeUnit')}
                        className="text-neutral-7 hover:text-red-6"
                        onClick={() =>
                          retarget.mutate((current) =>
                            current.filter((x) => x !== id),
                          )
                        }
                      >
                        ×
                      </button>
                    )}
                  </span>
                ))}
              </div>
            ) : (
              <p className="text-xs text-neutral-6">
                {t('pages.utilities.unassigned')}
              </p>
            )}
            {canManage &&
              (addableOUs.length > 0 ? (
                <select
                  value=""
                  disabled={retarget.isPending}
                  aria-label={t('pages.utilities.sidebar.addUnit')}
                  onChange={(e) => {
                    if (e.target.value) {
                      {
                        const added = e.target.value
                        retarget.mutate((current) =>
                          current.includes(added)
                            ? current
                            : [...current, added],
                        )
                      }
                    }
                  }}
                  className="rounded border border-neutral-11 bg-neutral-13 px-2 py-1.5 text-sm text-neutral-2"
                >
                  <option value="">
                    {t('pages.utilities.sidebar.addUnit')}
                  </option>
                  {addableOUs.map((ou) => (
                    <option key={ou.id} value={ou.id}>
                      {ou.name}
                    </option>
                  ))}
                </select>
              ) : (
                <p className="text-xs text-neutral-6">
                  {t('pages.utilities.sidebar.noMoreUnits')}
                </p>
              ))}
          </div>
        ) : (
          <div className="flex items-center gap-2">
            <span className="text-neutral-7 w-32 flex-shrink-0">
              {t('pages.utilities.sidebar.scope')}
            </span>
            <span className="text-neutral-2 truncate">
              {scopeLabel(assignment)}
            </span>
          </div>
        )}
        {definition?.min_agent_version && (
          <div className="flex items-center gap-2">
            <span className="text-neutral-7 w-32 flex-shrink-0">
              {t('pages.utilities.sidebar.minAgent')}
            </span>
            <span className="text-neutral-4">
              ≥ {definition.min_agent_version}
            </span>
          </div>
        )}
        {(definition?.depends_on?.length ?? 0) > 0 && (
          <div className="flex items-center gap-2">
            <span className="text-neutral-7 w-32 flex-shrink-0">
              {t('pages.utilities.sidebar.dependsOn')}
            </span>
            <span className="text-neutral-4">
              {definition?.depends_on?.join(', ')}
            </span>
          </div>
        )}
      </div>

      <label className="flex items-center gap-2 text-sm text-neutral-2 cursor-pointer select-none border-t border-neutral-12 pt-4">
        <input
          type="checkbox"
          checked={enabled}
          disabled={!canManage}
          onChange={(e) => {
            setEnabled(e.target.checked)
            setEnabledTouched(true)
            setDirty(true)
          }}
        />
        {t('pages.utilities.sidebar.enabled')}
      </label>

      {params.length > 0 ? (
        <div className="flex flex-col gap-3">
          <h3 className="text-xs uppercase font-bold text-neutral-4">
            {t('pages.utilities.sidebar.configuration')}
          </h3>
          {params.map((param) => (
            <UtilityParamField
              key={param.name}
              param={param}
              value={config[param.name]}
              disabled={!canManage}
              onChange={(value) => {
                setConfig({ ...config, [param.name]: value })
                setDirty(true)
              }}
              onClear={() => {
                const next = { ...config }
                delete next[param.name]
                setConfig(next)
                setDirty(true)
              }}
            />
          ))}
        </div>
      ) : (
        <div className="text-sm text-neutral-6">
          {t('pages.utilities.sidebar.noParams')}
        </div>
      )}

      {canManage && (
        <div className="flex gap-2 border-t border-neutral-12 pt-4">
          <Button
            grow
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate()}
          >
            {save.isPending
              ? t('pages.utilities.sidebar.saving')
              : t('pages.utilities.sidebar.save')}
          </Button>
          <Button
            grow
            kind="secondary"
            disabled={apply.isPending || dirty || !assignment?.enabled}
            title={
              dirty
                ? t('pages.utilities.sidebar.applyDirtyHint')
                : !assignment?.enabled
                  ? t('pages.utilities.sidebar.applyDisabledHint')
                  : t('pages.utilities.sidebar.applyHint')
            }
            onClick={() => apply.mutate()}
          >
            {apply.isPending
              ? t('pages.utilities.sidebar.applying')
              : t('pages.utilities.sidebar.apply')}
          </Button>
        </div>
      )}

      {applyResults !== null && (
        <div className="flex flex-col gap-2 rounded border border-neutral-11 bg-neutral-13 p-3">
          <div className="text-xs font-medium uppercase tracking-wide text-neutral-5">
            {t('pages.utilities.applyResults.title')}
          </div>
          {applyResults.length === 0 ? (
            <div className="text-sm text-neutral-6">
              {t('pages.utilities.applyResults.empty')}
            </div>
          ) : (
            applyResults.map((r) => (
              <div
                key={r.node_id}
                className="flex items-start justify-between gap-2 text-sm"
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
      )}
    </div>
  )
}
