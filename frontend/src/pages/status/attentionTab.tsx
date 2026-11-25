import { useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SidebarEnum } from '@/widgets/sidebars/types'

import {
  accountLimitAttentionDetail,
  deviceCapTenantDetail,
} from '@/entities/entitlement/deviceCap'
import {
  isDismissed,
  setDismissals,
  undismissedCountsByKind,
  useDismissals,
  useFleetAttention,
  withDismissal,
  withoutDismissal,
  type AttentionItem,
  type AttentionNode,
} from '@/entities/fleet'
import {
  DISPATCH_REMEDIES,
  REMEDIES,
  remedyForItem,
  type Remedy,
  type RemedyOutcome,
  type RemedyTarget,
} from '@/entities/fleet/remedies'
import { useOUs } from '@/entities/ou'
import { useCan } from '@/entities/permissions/hooks'

import Badge from '@/shared/ui/badge'
import { Button } from '@/shared/ui/button/button.tsx'
import { Column, DataTable } from '@/shared/ui/dataTable'
import { InfoHint } from '@/shared/ui/infoHint'
import { showError } from '@/shared/ui/toast'

import {
  FailureDialog,
  RemedyConfirmDialog,
  RemedyResultsDialog,
  type PendingRemedy,
} from './remedyDialogs'
import { parseCsv, routeApi, severityColor, toCsv } from './shared'

// A subject longer than this is a sentence, not a value — the boot-order
// refusal is the only one — so it moves behind the info mark instead of
// stretching the column. Destructive facts never hide behind hover, and none
// of these are: they explain a row that is already labelled and coloured.
const INLINE_SUBJECT_MAX = 48

// How long the page keeps watching for a dispatched remedy to show up in the
// answer before it stops asking.
const SETTLE_TIMEOUT_MS = 3 * 60 * 1000

// The states the platform will dispatch a non-queueable command in. Anything
// else is refused with "node is not available (current status: …)", so a
// button offered outside these can only produce an error.
const DISPATCHABLE = new Set(['online', 'maintenance'])

// The only command that really takes the device down. The platform pins a node
// to "restarting" for the EEPROM commands too — pessimistically, because it
// cannot trust an agent about a reboot it has not performed yet — and releases
// it the moment the agent answers rebooting=false, which for a boot-order
// flash is about thirteen seconds later. Calling that window "restarting" tells
// an operator the machine is going down when it is not.
const REBOOTING_SLUG = 'reboot'

type Props = {
  /** The unit filter from the rail, already resolved to a server value. */
  ouParam?: string
}

export const AttentionTab = ({ ouParam }: Props) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = routeApi.useSearch()
  const { ous } = useOUs()
  const check = useCan()

  // Conditions this page has just acted on, as `nodeId:kind`. A dispatched
  // remedy does not clear its row: the platform accepts the command and the
  // condition holds until the device reports back. Marking them keeps the row
  // honest — "sent, waiting" rather than an untouched warning with a button
  // that looks inert — and drives a short poll until the answer catches up.
  const [settling, setSettling] = useState<Set<string>>(new Set())
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [pending, setPending] = useState<PendingRemedy | null>(null)
  const [busy, setBusy] = useState(false)
  const [outcomes, setOutcomes] = useState<RemedyOutcome[] | null>(null)
  const [failureCommandId, setFailureCommandId] = useState<string | null>(null)
  // From the shared store, not local state: the nav badge counts the same set
  // and has to drop the number the moment a row is hidden here.
  const dismissals = useDismissals()
  // Hidden rows STAY in the list by default — hiding takes a condition out of
  // the count, not out of sight. The filter is what removes them from view.
  const [showDismissed, setShowDismissed] = useState(true)

  const { data, isLoading, isError, refetch } = useFleetAttention(
    ouParam,
    settling.size > 0,
  )

  // A mark clears when the server stops reporting that condition for that
  // node. Keyed off the DATA, not a timer: the fix has landed exactly when the
  // list says so, and an update that takes two minutes is as well served as
  // one that takes two seconds.
  useEffect(() => {
    if (settling.size === 0) {
      return
    }
    // Give up after a few minutes. Most remedies land in seconds, but some
    // conditions clear only on an operator action that may be tomorrow — a
    // flashed bootloader waits for a reboot — and a page that polls until then
    // is a page nobody should leave open.
    const stop = setTimeout(() => setSettling(new Set()), SETTLE_TIMEOUT_MS)
    return () => clearTimeout(stop)
  }, [settling])

  useEffect(() => {
    if (!data || settling.size === 0) {
      return
    }
    const live = new Set(
      data.nodes.flatMap((n) => n.items.map((i) => `${n.node_id}:${i.kind}`)),
    )
    const remaining = [...settling].filter((mark) => live.has(mark))
    if (remaining.length !== settling.size) {
      setSettling(new Set(remaining))
    }
  }, [data, settling])

  const selectedKinds = useMemo(
    () => parseCsv(search.conditions),
    [search.conditions],
  )

  const unitName = useMemo(() => {
    const byId = new Map(ous.map((ou) => [ou.id, ou.name]))
    return (id?: string) => (id ? byId.get(id) : undefined)
  }, [ous])

  // Filtering the ITEMS, not just the rows: with a condition selected, a node
  // that also has three unrelated ones should show the one that was asked for.
  // A node whose items all fall outside the filter drops out entirely.
  const rows = useMemo(() => {
    const wanted = selectedKinds.length ? new Set(selectedKinds) : null
    return (data?.nodes ?? [])
      .map((n) => ({
        ...n,
        items: n.items.filter(
          (i) =>
            (!wanted || wanted.has(i.kind)) &&
            // Hidden is a state, not a deletion: the row stays unless the
            // filter says otherwise.
            (showDismissed || !isDismissed(dismissals, n.node_id, i)),
        ),
      }))
      .filter((n) => n.items.length > 0)
  }, [data?.nodes, selectedKinds, dismissals, showDismissed])

  // How many conditions this browser is hiding, so the toggle can say so
  // rather than being a switch with no indication anything is behind it.
  const dismissedCount = useMemo(
    () =>
      (data?.nodes ?? []).reduce(
        (total, n) =>
          total +
          n.items.filter((i) => isDismissed(dismissals, n.node_id, i)).length,
        0,
      ),
    [data?.nodes, dismissals],
  )

  // The same function the nav badge sums, so the chips and the badge cannot
  // disagree about what "hidden" removed.
  const visibleCounts = useMemo(
    () => undismissedCountsByKind(data?.nodes ?? [], dismissals),
    [data?.nodes, dismissals],
  )

  /** Hides every condition currently shown on one node.
   *
   *  Per ROW rather than per condition: the control lives in its own column
   *  and a column has one cell per row. Granularity is not lost where it
   *  matters, because a dismissal is keyed to the VALUE — a condition that
   *  changes, or a new one appearing on the same node, is not covered by it. */
  const hideRow = (row: AttentionNode) => {
    let next = dismissals
    for (const item of row.items) {
      next = withDismissal(next, row.node_id, item)
    }
    setDismissals(next)
  }

  const restoreRow = (row: AttentionNode) => {
    let next = dismissals
    for (const item of row.items) {
      next = withoutDismissal(next, row.node_id, item.kind)
    }
    setDismissals(next)
  }

  /** May this caller run this remedy on this node, and would it do anything?
   *
   *  Two gates. The permission is the node's own unit, exactly as the node
   *  sidebar's buttons ask it. The second is the boot state: every apply in
   *  the platform's managed-OS-only set is HELD while a node runs the recovery
   *  image, so the command would sit registered with no delivery attempt —
   *  a button that appears to do nothing. */
  const allowed = useCallback(
    (remedy: Remedy, node: AttentionNode) => {
      if (
        node.in_recovery &&
        (remedy.managedOSOnly || remedy.refusedInRecovery)
      ) {
        return false
      }
      if (remedy.requiresConnected && !DISPATCHABLE.has(node.status)) {
        return false
      }
      // The platform is mid-something here. A second dispatch behind one that
      // has the agent restarting is delivered and never acknowledged.
      if (node.busy) {
        return false
      }
      return check(remedy.action, node.ou_id)
    },
    [check],
  )

  // What each remedy would reach across the current selection. Nodes the
  // caller may not act on are COUNTED rather than dropped: a button that
  // quietly does less than it says is worse than one that says so.
  const bulk = useMemo(() => {
    const chosen = rows.filter((n) => selected.has(n.node_id))
    return DISPATCH_REMEDIES()
      .map((remedy) => {
        const withKind = chosen.filter((n) =>
          n.items.some((i) => i.kind === remedy.kind),
        )
        const eligible = withKind.filter((n) => allowed(remedy, n))
        return {
          remedy,
          eligible: eligible.map((n): RemedyTarget => ({
            nodeId: n.node_id,
            nodeName: n.node_name,
          })),
          deniedCount: withKind.length - eligible.length,
        }
      })
      .filter((b) => b.eligible.length > 0 || b.deniedCount > 0)
  }, [rows, selected, allowed])

  const run = async (remedy: Remedy, targets: RemedyTarget[]) => {
    if (!remedy.run) {
      return
    }
    setBusy(true)
    try {
      const result = await remedy.run(targets)
      setOutcomes(result)
      // Only what the platform accepted. A failed node keeps its plain
      // warning, because nothing is on its way to it.
      setSettling((prev) => {
        const next = new Set(prev)
        for (const r of result) {
          if (r.state !== 'failed') {
            next.add(`${r.nodeId}:${remedy.kind}`)
          }
        }
        return next
      })
      // Keep only what did not land, so a retry repeats the failures rather
      // than the whole selection.
      const stuck = new Set(
        result.filter((r) => r.state === 'failed').map((r) => r.nodeId),
      )
      setSelected((prev) => new Set([...prev].filter((id) => stuck.has(id))))
      void refetch()
    } catch (error) {
      showError(t('pages.status.remedies.failed'), error)
    } finally {
      setBusy(false)
      setPending(null)
    }
  }

  const start = (
    remedy: Remedy,
    eligible: RemedyTarget[],
    deniedCount: number,
    confirm: boolean,
  ) => {
    if (!eligible.length) {
      return
    }
    if (confirm) {
      setPending({
        remedyId: remedy.id,
        eligible,
        deniedCount,
        reboots: !!remedy.reboots,
        acknowledge: !!remedy.acknowledge,
      })
      return
    }
    void run(remedy, eligible)
  }

  const toggleKind = (kind: string) => {
    const next = selectedKinds.includes(kind)
      ? selectedKinds.filter((k) => k !== kind)
      : [...selectedKinds, kind]
    void navigate({
      to: '.',
      search: (prev) => ({ ...prev, conditions: toCsv(next) }),
      replace: true,
    })
  }

  const toggleRow = (nodeId: string) =>
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(nodeId)) {
        next.delete(nodeId)
      } else {
        next.add(nodeId)
      }
      return next
    })

  const allSelected =
    rows.length > 0 && rows.every((n) => selected.has(n.node_id))

  const kindLabel = (kind: string) =>
    // A kind the server added and this release has no words for renders as its
    // own identifier rather than disappearing from the filter.
    t(`pages.status.attention.kinds.${kind}` as never, { defaultValue: kind })

  const itemDetail = (item: AttentionItem) => {
    if (item.kind === 'account_device_limit_reached') {
      // The account's own limit and count; nothing of the install's.
      const line = accountLimitAttentionDetail(item)
      return t(`entities.entitlement.deviceCap.${line.key}`, line.values ?? {})
    }
    if (item.kind === 'device_cap_reached') {
      // A non-operator sees the fact and its own count, never the install's.
      const tenant = deviceCapTenantDetail(item)
      if (tenant) {
        return tenant
          .map((line) =>
            t(`entities.entitlement.deviceCap.${line.key}`, line.values ?? {}),
          )
          .join(' ')
      }
      return t('pages.status.attention.deviceCapDetail', {
        admitted: item.current ?? '',
        cap: item.expected ?? '',
      })
    }
    if (item.current && item.expected) {
      return `${item.current} → ${item.expected}`
    }
    if (item.count) {
      return String(item.count)
    }
    return item.subject ?? ''
  }

  const columns: Column<AttentionNode>[] = [
    {
      key: 'select',
      header: '',
      alwaysVisible: true,
      className: 'w-8',
      render: (row) => (
        <input
          type="checkbox"
          checked={selected.has(row.node_id)}
          // The row itself opens the node panel; a checkbox that also did
          // that could never be ticked.
          onClick={(e) => e.stopPropagation()}
          onChange={() => toggleRow(row.node_id)}
          aria-label={t('pages.status.attention.selectNode', {
            node: row.node_name,
          })}
          className="accent-lime-6"
        />
      ),
    },
    {
      key: 'node',
      header: t('pages.status.attention.table.node'),
      alwaysVisible: true,
      render: (row) => (
        <div className="flex flex-col">
          <span className="text-neutral-2">{row.node_name}</span>
          {row.hostname && (
            <span className="text-xs text-neutral-6">{row.hostname}</span>
          )}
        </div>
      ),
      sortValue: (row) => row.node_name.toLowerCase(),
    },
    {
      key: 'unit',
      header: t('pages.status.attention.table.unit'),
      render: (row) =>
        unitName(row.ou_id) ?? (
          <span className="text-neutral-6">
            {t('pages.status.attention.table.noUnit')}
          </span>
        ),
      sortValue: (row) => (unitName(row.ou_id) ?? '').toLowerCase(),
    },
    {
      key: 'conditions',
      header: t('pages.status.attention.table.conditions'),
      alwaysVisible: true,
      render: (row) => (
        <div className="flex flex-col gap-1">
          {row.items.map((item) => {
            const detail = itemDetail(item)
            const long = detail.length > INLINE_SUBJECT_MAX
            const remedy = remedyForItem(item)
            const canFix = !!remedy && allowed(remedy, row)
            const sent = settling.has(`${row.node_id}:${item.kind}`)
            // Withheld rather than unpermitted, and the two reasons differ:
            // one is queued until the OS returns, the other is refused by the
            // ROM outright. Say which, or the missing button reads as a
            // permission the operator does not have.
            const heldByRecovery =
              !!remedy && !!remedy.managedOSOnly && !!row.in_recovery
            const refusedByRecovery =
              !!remedy && !!remedy.refusedInRecovery && !!row.in_recovery
            // Already doing it, or not there to be asked. Both are refusals
            // the platform would make, and the first is the one that looked
            // like a live button on a node that was mid-reboot.
            const notDispatchable =
              !!remedy &&
              !!remedy.requiresConnected &&
              !heldByRecovery &&
              !refusedByRecovery &&
              !DISPATCHABLE.has(row.status)
            // The last try at THIS remedy, if it failed. The row says so and
            // offers the agent's own words rather than leaving an operator to
            // press the same button again — a remedy that fails (an unwritable
            // boot partition, say) otherwise leaves nothing on screen to show it.
            const dismissedHere = isDismissed(dismissals, row.node_id, item)
            const failure = remedy?.slug
              ? row.failures?.find((f) => f.slug === remedy.slug)
              : undefined
            const nodeBusy =
              !!remedy &&
              !!row.busy &&
              !heldByRecovery &&
              !refusedByRecovery &&
              !notDispatchable
            return (
              <div
                key={`${item.kind}:${item.subject ?? ''}`}
                className={clsx(
                  'flex items-center gap-2 flex-wrap',
                  // Still listed, no longer counted. Dimming is the only thing
                  // that distinguishes the two, so it has to be visible.
                  dismissedHere && 'opacity-40',
                )}
              >
                <Badge
                  variant="outline"
                  color={
                    // The one condition that is a MODE rather than a fault
                    // shares the state column's colour, so the row reads as
                    // one fact instead of a warning next to a state.
                    item.kind === 'in_recovery'
                      ? 'violet'
                      : severityColor(item.severity)
                  }
                >
                  {kindLabel(item.kind)}
                </Badge>
                {detail &&
                  (long ? (
                    <InfoHint text={detail} />
                  ) : (
                    <span className="font-mono text-xs text-neutral-5">
                      {detail}
                    </span>
                  ))}
                {sent && (
                  <span className="text-xs text-neutral-4">
                    {t('pages.status.remedies.sent')}
                  </span>
                )}
                {!sent && nodeBusy && row.busy && (
                  <span className="whitespace-nowrap text-xs text-neutral-6">
                    {/* Name what is running. "Busy" alone is
                        indistinguishable from stuck, and a package install
                        runs for minutes at a time. */}
                    {t('pages.status.remedies.nodeBusyRunning', {
                      command: row.busy.slug,
                    })}
                    {!!row.busy.progress && ` · ${row.busy.progress}%`}
                    &nbsp;
                    <InfoHint text={t('pages.status.remedies.nodeBusyHint')} />
                  </span>
                )}
                {!sent && notDispatchable && (
                  <span className="whitespace-nowrap text-xs text-neutral-6">
                    {row.status === 'restarting'
                      ? t('pages.status.remedies.alreadyRestarting')
                      : t('pages.status.remedies.needsOnline')}
                    &nbsp;
                    <InfoHint
                      text={t('pages.status.remedies.needsOnlineHint')}
                    />
                  </span>
                )}
                {!sent && (heldByRecovery || refusedByRecovery) && (
                  <span className="whitespace-nowrap text-xs text-neutral-6">
                    {heldByRecovery
                      ? t('pages.status.remedies.heldInRecovery')
                      : t('pages.status.remedies.refusedInRecovery')}
                    &nbsp;
                    <InfoHint
                      text={
                        heldByRecovery
                          ? t('pages.status.remedies.heldInRecoveryHint')
                          : t('pages.status.remedies.refusedInRecoveryHint')
                      }
                    />
                  </span>
                )}
                {!sent && failure && (
                  <button
                    type="button"
                    onClick={(e) => {
                      e.stopPropagation()
                      setFailureCommandId(failure.command_id)
                    }}
                    className="text-xs text-red-5 hover:text-red-4 underline"
                  >
                    {t('pages.status.remedies.lastAttemptFailed')}
                  </button>
                )}
                {!sent && canFix && remedy && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={(e) => {
                      e.stopPropagation()
                      if (remedy.opens) {
                        // A place, not an action: nothing to confirm.
                        void navigate(remedy.opens)
                        return
                      }
                      start(
                        remedy,
                        [{ nodeId: row.node_id, nodeName: row.node_name }],
                        0,
                        // One node, one click: confirm where the device goes
                        // down or where the action asks to be acknowledged.
                        // A confirmation on every safe action is one nobody
                        // reads, so the cheap ones stay one click.
                        !!remedy.reboots || !!remedy.acknowledge,
                      )
                    }}
                    className="text-xs text-lime-6 hover:text-lime-5 underline disabled:opacity-50"
                  >
                    {t(`pages.status.remedies.${remedy.id}.label` as never)}
                  </button>
                )}
              </div>
            )
          })}
        </div>
      ),
    },
    {
      key: 'hide',
      header: t('pages.status.attention.table.hide'),
      alwaysVisible: true,
      className: 'w-24',
      render: (row) => {
        const allHidden =
          row.items.length > 0 &&
          row.items.every((i) => isDismissed(dismissals, row.node_id, i))
        return (
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              if (allHidden) {
                restoreRow(row)
              } else {
                hideRow(row)
              }
            }}
            title={
              allHidden
                ? t('pages.status.attention.restoreHint')
                : t('pages.status.attention.dismissHint')
            }
            className="text-xs text-neutral-5 hover:text-neutral-2 underline whitespace-nowrap"
          >
            {allHidden
              ? t('pages.status.attention.restore')
              : t('pages.status.attention.dismiss')}
          </button>
        )
      },
    },
    {
      key: 'status',
      header: t('pages.status.attention.table.status'),
      // A netbooted node says "online", and it is — but what decides what can
      // be done to it is the image it is running, not its reachability. Violet
      // is already this product's colour for recovery (the batch menu's
      // reboot-to-recovery row), so the column says recovery and says it in
      // violet rather than leaving the operator to read it off a badge.
      render: (row) => {
        if (row.in_recovery) {
          return (
            <span className="font-mono text-xs text-violet-5">
              {t('pages.status.attention.table.recoveryState')}
            </span>
          )
        }
        // Pinned for work that is not a reboot: say so, and say in the tooltip
        // that the platform cannot know yet — an older agent, or a run on
        // different hardware, may still go down.
        const heldForWork =
          row.status === 'restarting' &&
          !!row.busy &&
          row.busy.slug !== REBOOTING_SLUG
        if (heldForWork) {
          return (
            <span
              className="font-mono text-xs text-blue-6"
              title={t('pages.status.attention.table.heldStateHint', {
                command: row.busy?.slug ?? '',
              })}
            >
              {t('pages.status.attention.table.heldState')}
            </span>
          )
        }
        return (
          <span className="font-mono text-xs text-neutral-5">{row.status}</span>
        )
      },
      sortValue: (row) =>
        row.in_recovery
          ? 'recovery'
          : row.status === 'restarting' &&
              row.busy &&
              row.busy.slug !== REBOOTING_SLUG
            ? 'working'
            : row.status,
    },
    {
      key: 'since',
      header: t('pages.status.attention.table.since'),
      defaultHidden: true,
      render: (row) => {
        const since = row.items.find((i) => i.since)?.since
        return since ? (
          <span className="font-mono text-xs text-neutral-5">
            {new Date(since).toLocaleString()}
          </span>
        ) : (
          ''
        )
      },
      sortValue: (row) => row.items.find((i) => i.since)?.since ?? '',
    },
  ]

  if (isError) {
    return (
      <p className="text-sm text-red-5">{t('pages.status.attention.failed')}</p>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {/* The filter is built from the server's inventory, so a check added
          there appears here without a frontend release. */}
      <div className="flex flex-wrap gap-2">
        {(data?.kinds ?? []).map((kind) => {
          // The server counts every item; this browser's hidden ones are
          // subtracted here so the chips, the nav badge and the word "hidden"
          // all describe the same set.
          const count = visibleCounts[kind] ?? 0
          const active = selectedKinds.includes(kind)
          return (
            <button
              key={kind}
              type="button"
              onClick={() => toggleKind(kind)}
              disabled={count === 0 && !active}
              className={clsx(
                'rounded-[3px] border px-2 py-[3px] text-xs transition-colors',
                active
                  ? 'border-lime-6 text-lime-6'
                  : count === 0
                    ? 'border-neutral-12 text-neutral-8 cursor-default'
                    : 'border-neutral-11 text-neutral-4 hover:text-neutral-2',
              )}
            >
              {kindLabel(kind)}
              <span className="ml-1.5 font-mono">{count}</span>
            </button>
          )
        })}
        {selectedKinds.length > 0 && (
          <button
            type="button"
            onClick={() =>
              void navigate({
                to: '.',
                search: (prev) => ({ ...prev, conditions: undefined }),
                replace: true,
              })
            }
            className="text-xs text-neutral-4 hover:text-neutral-2 underline px-1"
          >
            {t('pages.status.attention.clearFilter')}
          </button>
        )}
        {/* Hidden rows are listed by default. This is what takes them out of
            view — and it only appears when there is something to take out, so
            the filter row does not carry a control that does nothing. */}
        {dismissedCount > 0 && (
          <button
            type="button"
            onClick={() => setShowDismissed((v) => !v)}
            className={clsx(
              'rounded-[3px] border px-2 py-[3px] text-xs transition-colors',
              showDismissed
                ? 'border-neutral-11 text-neutral-4 hover:text-neutral-2'
                : 'border-lime-6 text-lime-6',
            )}
          >
            {showDismissed
              ? t('pages.status.attention.hideDismissed', {
                  count: dismissedCount,
                })
              : t('pages.status.attention.showDismissed', {
                  count: dismissedCount,
                })}
          </button>
        )}
      </div>

      {/* An incomplete list must not read as a clean fleet. */}
      {!!data?.omitted?.length && (
        <p className="text-sm text-yellow-6">
          {t('pages.status.attention.omitted', {
            checks: data.omitted.map(kindLabel).join(', '),
          })}
        </p>
      )}

      {/* Group actions. One button per remedy the selection can actually use,
          each labelled with how many nodes it would reach — so the operator
          reads the scope off the button rather than inferring it. */}
      {rows.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 border border-neutral-12 rounded p-2">
          <Button
            kind="tertiary"
            size="sm"
            onClick={() =>
              setSelected(
                allSelected ? new Set() : new Set(rows.map((n) => n.node_id)),
              )
            }
          >
            {allSelected
              ? t('pages.status.attention.deselectAll')
              : t('pages.status.attention.selectAll')}
          </Button>
          <span className="text-xs text-neutral-6">
            {t('pages.status.attention.selectedCount', {
              count: selected.size,
            })}
          </span>
          {bulk.map(({ remedy, eligible, deniedCount }) => (
            <Button
              key={remedy.id}
              kind="secondary"
              size="sm"
              disabled={busy || eligible.length === 0}
              onClick={() => start(remedy, eligible, deniedCount, true)}
            >
              {t(`pages.status.remedies.${remedy.id}.label` as never)}
              <span className="ml-1.5 font-mono">{eligible.length}</span>
            </Button>
          ))}
        </div>
      )}

      <DataTable
        storageKey="status-attention-table"
        columnsLabel={t('pages.status.columns')}
        columns={columns}
        rows={rows}
        rowKey={(row) => row.node_id}
        isSelected={(row) =>
          search.sidebar === SidebarEnum.Node && search.nodeId === row.node_id
        }
        onRowClick={(row) =>
          void navigate({
            to: '.',
            search: (prev) => ({
              ...prev,
              sidebar: SidebarEnum.Node,
              nodeId: row.node_id,
            }),
          })
        }
        empty={
          <p className="text-sm text-neutral-6">
            {isLoading
              ? t('pages.status.loading')
              : selectedKinds.length
                ? t('pages.status.attention.emptyFiltered')
                : t('pages.status.attention.empty')}
          </p>
        }
      />

      <RemedyConfirmDialog
        pending={pending}
        busy={busy}
        onConfirm={() => {
          if (pending) {
            const remedy = REMEDIES.find((r) => r.id === pending.remedyId)
            if (remedy) {
              void run(remedy, pending.eligible)
            }
          }
        }}
        onCancel={() => setPending(null)}
      />
      <RemedyResultsDialog
        outcomes={outcomes}
        onClose={() => setOutcomes(null)}
      />
      <FailureDialog
        commandId={failureCommandId}
        onClose={() => setFailureCommandId(null)}
      />
    </div>
  )
}
