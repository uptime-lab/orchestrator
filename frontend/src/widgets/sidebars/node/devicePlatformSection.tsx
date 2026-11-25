import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import clsx from 'clsx'
import { t } from 'i18next'
import { FC, useCallback, useEffect, useMemo, useState } from 'react'

import {
  applyDeviceProfile,
  getDeviceProfileCatalog,
  getDeviceProfilePreview,
  getDeviceProfileScan,
  getNodeDeviceProfile,
  putNodeDeviceProfile,
  scanDeviceProfile,
  type DeviceProfileBoard,
  type DeviceProfileOptionStatus,
  type EquippedModule,
  type NodeDeviceProfile,
} from '@/entities/deviceProfile'
import { postNodeCommand } from '@/entities/node/api/postNodeCommand'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'

import { SectionDivider } from './infoRow.tsx'

// PLATFORM section (Properties tab): declare the carrier board, factory
// variant and plugged modules; apply the rendered config.txt block; confirm
// with a scan. The declaration is manual on purpose — a carrier board is not
// detectable, and a TPM/RTC is invisible to the kernel until its overlay is
// applied — so detection only CONFIRMS, never chooses.
//
// The whole section renders nothing when the node has no compatible profile
// and nothing assigned: a plain Pi user never sees it.

// The scan is a dispatched command, so the answer arrives asynchronously
// (the watchdogLiveStatus shape and its bounds).
const POLL_MS = 2000
// A scan that never reaches a terminal state (node dies mid-scan, poll keeps
// failing) must not show "Scanning…" forever on a 2s loop.
const POLL_DEADLINE_MS = 60_000

const statusClassName: Record<DeviceProfileOptionStatus, string> = {
  declared: 'border-neutral-8/50 bg-neutral-8/10 text-neutral-5',
  update_pending: 'border-amber-6/40 bg-amber-6/10 text-amber-6',
  applied: 'border-lime-7/40 bg-lime-7/10 text-lime-6',
  confirmed: 'border-green-8/50 bg-green-7/10 text-green-7',
  working: 'border-green-8/50 bg-green-7/10 text-green-7',
  not_confirmed: 'border-red-6/40 bg-red-6/10 text-red-6',
}

const sourceHuman = (source?: NodeDeviceProfile['source']): string | null => {
  switch (source) {
    case 'ou':
      return t('widgets.sidebars.node.devicePlatformSection.inheritedOU')
    case 'root_ou':
      return t('widgets.sidebars.node.devicePlatformSection.inheritedRootOU')
    case 'account':
      return t('widgets.sidebars.node.devicePlatformSection.inheritedAccount')
    default:
      return null
  }
}

export const DevicePlatformSection: FC<{
  nodeId: string
  canEdit: boolean // node.file_write — assign + apply
  canScan: boolean // node.status_check — scan
  canReboot: boolean // node.reboot — the activate-by-reboot button
  online: boolean
  // The node's boot moment from the live node push. A CHANGE means it
  // rebooted, which is exactly when reboot_pending stops being true.
  lastBootAt?: number
}> = ({ nodeId, canEdit, canScan, canReboot, online, lastBootAt }) => {
  const queryClient = useQueryClient()
  const [scanCommandId, setScanCommandId] = useState<string | null>(null)
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [confirmReboot, setConfirmReboot] = useState(false)
  // Board picker dropdown: searchable, because the registry is meant to grow
  // well past what a segmented button row can hold.
  const [pickerOpen, setPickerOpen] = useState(false)
  const [search, setSearch] = useState('')

  const { data: catalog } = useQuery({
    queryKey: ['device-profile-catalog'],
    queryFn: getDeviceProfileCatalog,
    staleTime: 5 * 60 * 1000, // registry content changes only with a deploy
  })
  const { data: state } = useQuery({
    queryKey: ['device-profile', nodeId],
    queryFn: () => getNodeDeviceProfile(nodeId),
  })

  const refresh = useCallback(
    () =>
      queryClient.invalidateQueries({ queryKey: ['device-profile', nodeId] }),
    [queryClient, nodeId],
  )

  // A reboot changes what the server says about this panel (reboot_pending),
  // and nothing else here would notice: the state query has no interval, so
  // after pressing Reboot the button came back and stayed offered until a
  // manual page refresh.
  //
  // The trigger is the node's BOOT MOMENT, not its online flag. Coming back
  // online is the earlier event — the agent reports its system info in a
  // separate frame afterwards, and that later write is what flips
  // reboot_pending — so refetching on "online" would race it and read the
  // stale true. A changed boot moment can only be observed after that write,
  // so this cannot be early. It is also the reason a reconnect that is NOT a
  // reboot (a dropped stream, a restarted backend) refetches nothing.
  useEffect(() => {
    if (lastBootAt === undefined) return
    void refresh()
  }, [lastBootAt, refresh])

  const assign = useMutation({
    mutationFn: (equipage: {
      profile_id: string
      variant_id?: string
      modules?: EquippedModule[]
    }) => putNodeDeviceProfile(nodeId, equipage),
    onSuccess: (data) => {
      setError(null)
      // Seed the cache with the returned state before invalidating: a second
      // toggle before the refetch lands must compute from this update, not
      // from the pre-mutation snapshot (lost update).
      queryClient.setQueryData(['device-profile', nodeId], data)
      void refresh()
      // The confirm dialog's preview renders the DECLARED equipage — a cached
      // render of the previous declaration must never be what an Apply shows.
      void queryClient.invalidateQueries({
        queryKey: ['device-profile-preview', nodeId],
      })
    },
    onError: (e: unknown) => setError(errText(e)),
  })

  const apply = useMutation({
    mutationFn: () => applyDeviceProfile(nodeId),
    onSuccess: (res) => {
      setConfirming(false)
      setError(null)
      setNotice(
        res.queued
          ? t('widgets.sidebars.node.devicePlatformSection.applyQueued')
          : t('widgets.sidebars.node.devicePlatformSection.applyDispatched'),
      )
      void refresh()
    },
    onError: (e: unknown) => {
      setConfirming(false)
      setError(errText(e))
    },
  })

  // The applied boot config takes effect only after a reboot, and the platform
  // never reboots by itself — this button is the operator doing it, behind its
  // own confirmation.
  const reboot = useMutation({
    mutationFn: () => postNodeCommand(nodeId, { command: 'reboot' }),
    onSuccess: () => {
      setConfirmReboot(false)
      setError(null)
      setNotice(
        t('widgets.sidebars.node.devicePlatformSection.rebootDispatched'),
      )
    },
    onError: (e: unknown) => {
      setConfirmReboot(false)
      setError(errText(e))
    },
  })

  // Preview is fetched only for the confirm dialog — its content IS the
  // consent: the operator sees the exact config.txt block before it ships.
  const preview = useQuery({
    queryKey: ['device-profile-preview', nodeId],
    queryFn: () => getDeviceProfilePreview(nodeId),
    enabled: confirming,
  })

  const scan = useMutation({
    mutationFn: () => scanDeviceProfile(nodeId),
    onSuccess: (commandId) => {
      setError(null)
      setNotice(null)
      setScanCommandId(commandId)
    },
    onError: (e: unknown) => setError(errText(e)),
  })

  // Poll the running scan; stop on terminal (the watchdogLiveStatus shape).
  const { data: scanState, error: scanPollError } = useQuery({
    queryKey: ['device-profile-scan', nodeId, scanCommandId],
    queryFn: () => getDeviceProfileScan(nodeId, scanCommandId as string),
    enabled: !!scanCommandId,
    refetchInterval: (query) => {
      // A poll error is terminal — the effect below surfaces it; retrying
      // forever shows "Scanning…" with nothing behind it.
      if (query.state.status === 'error') return false
      const s = query.state.data?.status
      return s === 'running' || s === undefined ? POLL_MS : false
    },
  })
  const scanRunning =
    !!scanCommandId &&
    scanState?.status !== 'failed' &&
    scanState?.status !== 'completed'
  // Terminal scan: fold the outcome into the main state once (effect, not
  // render — refresh() is a side effect). A poll error is terminal too.
  useEffect(() => {
    if (!scanCommandId) return
    if (scanPollError) {
      setScanCommandId(null)
      setError(errText(scanPollError))
      return
    }
    if (scanState?.status === 'completed' || scanState?.status === 'failed') {
      setScanCommandId(null)
      if (scanState.status === 'failed') {
        setError(
          scanState.detail ||
            t('widgets.sidebars.node.devicePlatformSection.scanIncomplete'),
        )
      }
      void refresh()
    }
  }, [scanCommandId, scanState, scanPollError, refresh])
  // The deadline clears the command id, which both stops the poll (the query
  // is enabled on it) and resets the "Scanning…" state — a refetchInterval
  // check alone leaves the button stuck once polling has silently stopped.
  useEffect(() => {
    if (!scanCommandId) return
    const timer = window.setTimeout(() => {
      setScanCommandId(null)
      setError(t('widgets.sidebars.node.devicePlatformSection.scanTimeout'))
    }, POLL_DEADLINE_MS)
    return () => window.clearTimeout(timer)
  }, [scanCommandId])

  const boards = catalog?.boards ?? []
  const compatible = useMemo(() => {
    const ids = new Set(state?.compatible_profiles ?? [])
    return boards.filter((b) => ids.has(b.id))
  }, [boards, state?.compatible_profiles])

  // Zero new UI unless this node can meaningfully use the feature.
  if (!state || (!state.assigned && compatible.length === 0)) return null

  const selectedBoard: DeviceProfileBoard | undefined = boards.find(
    (b) => b.id === state.profile_id,
  )
  const isNodeLevel = state.assigned && state.source === 'node'
  const inheritedFrom = sourceHuman(state.source)

  // Equipage edits are held while the confirm dialog is open: its preview is
  // the consent, and an edit behind it would let Apply ship a config the
  // operator never saw.
  const equipageLocked = assign.isPending || confirming

  // The picker writes the NODE's own declaration; INHERIT clears it.
  const pick = (profileId: string) => {
    if (!canEdit || equipageLocked) return
    if (profileId === '') {
      assign.mutate({ profile_id: '' })
      return
    }
    if (isNodeLevel && state.profile_id === profileId) return
    const board = boards.find((b) => b.id === profileId)
    const firstVariant = board?.variants?.[0]?.id
    assign.mutate({ profile_id: profileId, variant_id: firstVariant })
  }

  const pickVariant = (variantId: string) => {
    if (!canEdit || !state.profile_id || equipageLocked) return
    assign.mutate({
      profile_id: state.profile_id,
      variant_id: variantId,
      modules: state.modules,
    })
  }

  // Module UX: a module either is or is not equipped; the port is the
  // first one of the board (blades and generic boards have exactly one).
  const toggleModule = (moduleId: string) => {
    if (!canEdit || !state.profile_id || equipageLocked) return
    const port = selectedBoard?.ports?.[0]?.id
    if (!port) return
    const current = state.modules ?? []
    const next = current.some((m) => m.module_id === moduleId)
      ? current.filter((m) => m.module_id !== moduleId)
      : [...current, { module_id: moduleId, port_id: port }]
    assign.mutate({
      profile_id: state.profile_id,
      variant_id: state.variant_id,
      modules: next,
    })
  }

  const needsApply =
    state.assigned &&
    state.desired_hash !== undefined &&
    state.desired_hash !== state.applied_hash

  return (
    <div className="flex flex-col gap-3">
      {/* Board picker: a searchable dropdown, not a segmented row — the
          registry is meant to grow well past three boards. "Inherit" is the
          first option; while the node inherits, the trigger shows the
          resolved board with its source. */}
      <div className="relative">
        <button
          type="button"
          disabled={!canEdit || equipageLocked}
          onClick={() => {
            setPickerOpen((open) => !open)
            setSearch('')
          }}
          className={clsx(
            'w-full flex items-center justify-between gap-2 rounded border px-3 py-2 text-sm transition-colors disabled:opacity-50 disabled:cursor-not-allowed',
            'border-neutral-11 bg-neutral-13 hover:border-neutral-9',
            isNodeLevel ? 'text-lime-6' : 'text-orange-6',
          )}
        >
          <span className="truncate">
            {isNodeLevel
              ? (selectedBoard?.name ?? state.profile_id)
              : state.assigned
                ? t(
                    'widgets.sidebars.node.devicePlatformSection.inheritNamed',
                    {
                      name:
                        boards.find((b) => b.id === state.profile_id)?.name ??
                        state.profile_id,
                    },
                  )
                : t('widgets.sidebars.node.devicePlatformSection.inheritNone')}
          </span>
          <svg
            className={clsx(
              'w-3.5 h-3.5 flex-shrink-0 text-neutral-6 transition-transform',
              pickerOpen && 'rotate-180',
            )}
            fill="none"
            stroke="currentColor"
            strokeWidth={2}
            viewBox="0 0 24 24"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M19 9l-7 7-7-7"
            />
          </svg>
        </button>
        {pickerOpen && (
          <>
            {/* Click-outside backdrop. */}
            <div
              className="fixed inset-0 z-10"
              onClick={() => setPickerOpen(false)}
            />
            {/* In flow, not absolute: the sub-view's slide container clips
                overflow, so a floated list was cut off after one row. Opening
                pushes the panel down instead. */}
            <div className="relative z-20 mt-1 w-full rounded border border-neutral-11 bg-neutral-14 shadow-xl">
              <input
                autoFocus
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={t(
                  'widgets.sidebars.node.devicePlatformSection.searchPlaceholder',
                )}
                className="w-full bg-transparent border-b border-neutral-12 px-3 py-2 text-sm text-neutral-2 placeholder-neutral-7 outline-none"
              />
              <div className="max-h-56 overflow-y-auto py-1">
                {'inherit'.includes(search.trim().toLowerCase()) ||
                search.trim() === '' ? (
                  <button
                    type="button"
                    onClick={() => {
                      setPickerOpen(false)
                      pick('')
                    }}
                    className={clsx(
                      'w-full text-left px-3 py-2 text-sm hover:bg-neutral-13',
                      !isNodeLevel ? 'text-orange-6' : 'text-neutral-3',
                    )}
                  >
                    {t('widgets.sidebars.node.devicePlatformSection.inherit')}
                    <span className="block text-xs text-neutral-7">
                      {t(
                        'widgets.sidebars.node.devicePlatformSection.inheritHint',
                      )}
                    </span>
                  </button>
                ) : null}
                {compatible
                  .filter((b) => {
                    const q = search.trim().toLowerCase()
                    if (!q) return true
                    return (
                      b.name.toLowerCase().includes(q) ||
                      b.id.toLowerCase().includes(q) ||
                      (b.description ?? '').toLowerCase().includes(q)
                    )
                  })
                  .map((b) => (
                    <button
                      key={b.id}
                      type="button"
                      onClick={() => {
                        setPickerOpen(false)
                        pick(b.id)
                      }}
                      className={clsx(
                        'w-full text-left px-3 py-2 text-sm hover:bg-neutral-13',
                        isNodeLevel && state.profile_id === b.id
                          ? 'text-lime-6'
                          : 'text-neutral-3',
                      )}
                    >
                      {b.name}
                      {b.description && (
                        <span className="block text-xs text-neutral-7 truncate">
                          {b.description}
                        </span>
                      )}
                    </button>
                  ))}
                {compatible.length > 0 &&
                  search.trim() !== '' &&
                  !compatible.some((b) => {
                    const q = search.trim().toLowerCase()
                    return (
                      b.name.toLowerCase().includes(q) ||
                      b.id.toLowerCase().includes(q) ||
                      (b.description ?? '').toLowerCase().includes(q)
                    )
                  }) && (
                    <div className="px-3 py-2 text-xs text-neutral-6">
                      {t(
                        'widgets.sidebars.node.devicePlatformSection.noMatch',
                        { query: search.trim() },
                      )}
                    </div>
                  )}
              </div>
            </div>
          </>
        )}
      </div>

      {inheritedFrom && !isNodeLevel && (
        <div className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.devicePlatformSection.inheritedNote', {
            source: inheritedFrom,
          })}
        </div>
      )}

      {/* Factory variant of the selected board. */}
      {state.assigned && (selectedBoard?.variants?.length ?? 0) > 0 && (
        <div className="flex items-center gap-2">
          <span className="text-xs text-neutral-7 w-24 flex-shrink-0">
            {t('widgets.sidebars.node.devicePlatformSection.variant')}
          </span>
          <div className="flex gap-2 flex-1">
            {selectedBoard?.variants?.map((v) => (
              <button
                key={v.id}
                type="button"
                disabled={!canEdit || !isNodeLevel || equipageLocked}
                onClick={() => pickVariant(v.id)}
                title={v.notes}
                className={clsx(
                  'flex-1 rounded px-2 py-1.5 text-xs font-medium uppercase tracking-wide border transition-colors disabled:opacity-50 disabled:cursor-not-allowed',
                  state.variant_id === v.id
                    ? 'border-lime-7/50 bg-lime-7/10 text-lime-6'
                    : 'border-neutral-11 bg-neutral-13 text-neutral-4 hover:text-neutral-2 hover:border-neutral-9',
                )}
              >
                {v.name}
              </button>
            ))}
          </div>
        </div>
      )}

      {/* The ladder, per option. */}
      {state.assigned && (state.options?.length ?? 0) > 0 && (
        <div className="flex flex-col gap-1">
          {state.options?.map((o) => {
            const metaTitle = t(
              `widgets.sidebars.node.devicePlatformSection.status.${o.status}.title`,
            )
            // "confirmed" and "works" are persisted claims from the LAST scan
            // — say when that was, or an unplugged module keeps a green badge
            // forever.
            const seenOn = state.scanned_at
              ? new Date(state.scanned_at).toLocaleString()
              : null
            // Only an RTC can reach the working rung today, and naming what
            // the hardware is being USED for is worth more to an operator than
            // the generic sentence. Keyed on the capability rather than the
            // module id so it stays true as the registry grows.
            const workingTitle = o.capabilities?.includes('rtc')
              ? t('widgets.sidebars.node.devicePlatformSection.rtcWorkingTitle')
              : metaTitle
            const title =
              o.status === 'confirmed' && seenOn
                ? t(
                    'widgets.sidebars.node.devicePlatformSection.confirmedSeenOn',
                    { seenOn },
                  )
                : o.status === 'working'
                  ? seenOn
                    ? t(
                        'widgets.sidebars.node.devicePlatformSection.workingSeenOn',
                        { title: workingTitle, seenOn },
                      )
                    : workingTitle
                  : metaTitle
            return (
              <div
                key={`${o.kind}-${o.id}`}
                className="flex items-center gap-2 text-sm text-neutral-3"
              >
                <span className="flex-1 truncate">{o.name}</span>
                <span
                  title={title}
                  className={clsx(
                    'flex-shrink-0 text-xxs font-medium uppercase tracking-wide px-1.5 py-0.5 rounded border',
                    statusClassName[o.status],
                  )}
                >
                  {t(
                    `widgets.sidebars.node.devicePlatformSection.status.${o.status}.label`,
                  )}
                </span>
              </div>
            )
          })}
        </div>
      )}

      {/* Modules on the expansion port — their own subsection. */}
      {state.assigned && (selectedBoard?.ports?.length ?? 0) > 0 && (
        <div className="flex flex-col gap-1">
          <SectionDivider
            label={t('widgets.sidebars.node.devicePlatformSection.modules')}
          />
          {catalog?.modules?.map((m) => {
            const equipped = (state.modules ?? []).some(
              (x) => x.module_id === m.id,
            )
            return (
              <label
                key={m.id}
                title={m.description}
                className={clsx(
                  'flex items-center gap-2 text-sm rounded px-2 py-1.5 border',
                  equipped
                    ? 'border-neutral-11 bg-neutral-13 text-neutral-2'
                    : 'border-transparent text-neutral-5',
                  canEdit && isNodeLevel
                    ? 'cursor-pointer hover:border-neutral-9'
                    : 'cursor-default',
                )}
              >
                <input
                  type="checkbox"
                  checked={equipped}
                  disabled={!canEdit || !isNodeLevel || equipageLocked}
                  onChange={() => toggleModule(m.id)}
                  className="accent-lime-600"
                />
                <span className="flex-1 truncate">{m.name}</span>
                <span className="text-xxs text-neutral-7">
                  {selectedBoard?.ports?.[0]?.name}
                </span>
              </label>
            )
          })}
        </div>
      )}

      {error && <div className="text-xs text-red-6">{error}</div>}
      {notice && !error && (
        <div className="text-xs text-neutral-5">{notice}</div>
      )}
      <ConfirmDialog
        open={confirmReboot}
        title={t('widgets.sidebars.node.devicePlatformSection.reboot')}
        message={t(
          'widgets.sidebars.node.devicePlatformSection.rebootConfirmMessage',
        )}
        confirmText={t('widgets.sidebars.node.devicePlatformSection.reboot')}
        destructive
        busy={reboot.isPending}
        onConfirm={() => reboot.mutate()}
        onCancel={() => setConfirmReboot(false)}
      />

      {/* Apply + Scan. Apply is gated behind the preview confirm below. */}
      {state.assigned && (
        <div className="flex gap-2">
          {canEdit && (needsApply || !state.applied_hash) && (
            <Button
              variant="normal"
              grow
              disabled={apply.isPending || confirming}
              onClick={() => setConfirming(true)}
            >
              {t('widgets.sidebars.node.devicePlatformSection.apply')}
            </Button>
          )}
          {/* Applied but not yet activated: the dead grey button becomes the
              one action that actually finishes the job — reboot to activate the
              boot config.

              Gated on reboot_pending, which the server computes from the node's
              boot moment against the apply. Without it this button was offered
              FOREVER after any apply: it sat red and primary next to a red
              "not found" badge on a node that had already rebooted several
              times, reading as "reboot and this will be fixed" when a reboot
              could not fix it. Only an explicit false hides it — undefined
              means the server cannot tell, and withholding an action the
              operator needs is worse than offering a spare one. */}
          {!needsApply &&
            !!state.applied_hash &&
            state.reboot_pending !== false &&
            canReboot && (
              <Button
                variant="destructive"
                grow
                disabled={!online || reboot.isPending}
                title={
                  online
                    ? t(
                        'widgets.sidebars.node.devicePlatformSection.rebootTitleOnline',
                      )
                    : t(
                        'widgets.sidebars.node.devicePlatformSection.rebootTitleOffline',
                      )
                }
                onClick={() => setConfirmReboot(true)}
              >
                {reboot.isPending
                  ? t('widgets.sidebars.node.devicePlatformSection.rebooting')
                  : t('widgets.sidebars.node.devicePlatformSection.reboot')}
              </Button>
            )}
          {/* Nothing left to do here: either the operator cannot reboot, or the
              node has already booted into this config. Still shown so the row
              reports the state instead of collapsing to a lone Scan button. */}
          {!needsApply &&
            !!state.applied_hash &&
            canEdit &&
            (!canReboot || state.reboot_pending === false) && (
              <Button
                variant="normal"
                grow
                disabled
                title={
                  state.reboot_pending === false
                    ? t(
                        'widgets.sidebars.node.devicePlatformSection.activeTitle',
                      )
                    : t(
                        'widgets.sidebars.node.devicePlatformSection.appliedTitle',
                      )
                }
              >
                {state.reboot_pending === false
                  ? t('widgets.sidebars.node.devicePlatformSection.active')
                  : t('widgets.sidebars.node.devicePlatformSection.applied')}
              </Button>
            )}
          {canScan && (
            <Button
              variant="normal"
              kind="secondary"
              grow
              disabled={!online || scanRunning || scan.isPending}
              title={
                online
                  ? t(
                      'widgets.sidebars.node.devicePlatformSection.scanTitleOnline',
                    )
                  : t(
                      'widgets.sidebars.node.devicePlatformSection.scanTitleOffline',
                    )
              }
              onClick={() => scan.mutate()}
            >
              {scanRunning
                ? t('widgets.sidebars.node.devicePlatformSection.scanning')
                : t('widgets.sidebars.node.devicePlatformSection.scan')}
            </Button>
          )}
        </div>
      )}

      {/* Confirm: the exact block an apply writes, and the reboot notice.
          The content is the consent — never apply what was never shown. */}
      {confirming && (
        <div className="flex flex-col gap-2 rounded border border-neutral-11 bg-neutral-13 p-3">
          <div className="text-xs font-medium uppercase tracking-wide text-neutral-5">
            {t('widgets.sidebars.node.devicePlatformSection.writtenInto', {
              path: preview.data?.path ?? 'config.txt',
            })}
          </div>
          <pre className="text-xs text-neutral-3 bg-neutral-14 rounded p-2 overflow-x-auto whitespace-pre">
            {preview.data
              ? preview.data.content
              : t('widgets.sidebars.node.devicePlatformSection.rendering')}
          </pre>
          {(preview.data?.setup_kinds?.length ?? 0) > 0 && (
            <div className="text-xs text-neutral-5">
              {t('widgets.sidebars.node.devicePlatformSection.setupSteps', {
                steps: preview.data?.setup_kinds?.join(', '),
              })}
            </div>
          )}
          <div className="text-xs text-amber-6">
            {t(
              'widgets.sidebars.node.devicePlatformSection.takesEffectAfterReboot',
            )}
          </div>
          <div className="flex gap-2">
            <Button
              variant="normal"
              grow
              // isFetching, not just data presence: while a refetch is in
              // flight the cached render on screen may not be what an apply
              // would write.
              disabled={!preview.data || preview.isFetching || apply.isPending}
              onClick={() => apply.mutate()}
            >
              {preview.data?.up_to_date
                ? t('widgets.sidebars.node.devicePlatformSection.reApply')
                : t('widgets.sidebars.node.devicePlatformSection.apply')}
            </Button>
            <Button
              variant="normal"
              kind="secondary"
              onClick={() => setConfirming(false)}
            >
              {t('widgets.sidebars.node.devicePlatformSection.cancel')}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

const errText = (e: unknown): string => {
  if (typeof e === 'object' && e !== null) {
    const resp = (
      e as { response?: { data?: { message?: string; error?: string } } }
    ).response
    const msg = resp?.data?.message || resp?.data?.error
    if (msg) return msg
    const plain = (e as { message?: string }).message
    if (plain) return plain
  }
  return t('widgets.sidebars.node.devicePlatformSection.requestFailed')
}
