import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { t } from 'i18next'
import { FC, useState } from 'react'

import {
  disarmWatchdog,
  getWatchdog,
  getWatchdogPresets,
  putWatchdog,
  WATCHDOG_TARGET_TOKEN,
  type WatchdogConfig,
  type WatchdogPreset,
  type WatchdogPresetInfo,
  type WatchdogState,
} from '@/entities/node/api/watchdog'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { InfoHint } from '@/shared/ui/infoHint'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { InfoRow, SectionDivider } from './infoRow.tsx'
import { WatchdogForm } from './watchdogForm.tsx'
import { WatchdogLiveStatus } from './watchdogLiveStatus.tsx'

// Watchdog supervision for the installed OS. The device reboots itself when the
// thing being supervised stops answering, so this panel is deliberately built
// around presets: the configurations worth having are few, and the dangerous
// ones are easy to type by hand.
//
// It always shows two facts, never one — what was asked for, and what the
// device confirmed. A dispatched command is not an armed watchdog, a queued one
// has not been applied at all, and the agent can disarm itself if a config
// turns out to reboot-loop the node.

// The apply_watchdog_config command floor. The panel checks it pre-emptively so
// the user is told to update the agent instead of watching a dispatch fail.
const MIN_AGENT_VERSION = '0.4.0'

// This is a command FLOOR (three numeric components, fail-closed), a different
// question from entities/agent/version, which orders releases for the update
// offer. Fail-closed like the backend's MeetsMinAgentVersion: an empty,
// "latest" or unparseable version counts as too old.
const meetsMinAgentVersion = (version: string | undefined): boolean => {
  const parse = (v: string): number[] | null => {
    const m = /^v?(\d+)\.(\d+)\.(\d+)/.exec(v.trim())
    return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : null
  }
  const have = version ? parse(version) : null
  const want = parse(MIN_AGENT_VERSION)
  if (!have || !want) return false
  for (let i = 0; i < 3; i++) {
    if (have[i] !== want[i]) return have[i] > want[i]
  }
  return true
}

// The preset catalog comes from the server (GET /nodes/:id/watchdog-presets),
// including the sentence the confirmation must show. It used to be a copy in
// this file, which meant the wording describing what a device will do once
// armed could drift away from the bounds that enforce it.
const consequenceFor = (preset: WatchdogPresetInfo, target?: string): string =>
  preset.consequence
    .split(WATCHDOG_TARGET_TOKEN)
    .join(target || t('widgets.sidebars.node.watchdogSection.theTarget'))

// Where an inherited desired config came from, in the operator's words.
const watchdogSourceLabel = (source?: string): string | undefined => {
  switch (source) {
    case 'ou':
      return t('widgets.sidebars.node.watchdogSection.sourceOU')
    case 'root_ou':
      return t('widgets.sidebars.node.watchdogSection.sourceRootOU')
    case 'account':
      return t('widgets.sidebars.node.watchdogSection.sourceAccount')
    default:
      return undefined
  }
}

const describe = (state: WatchdogState): string => {
  const d = state.desired
  if (!d || !d.enabled)
    return t('widgets.sidebars.node.watchdogSection.notArmed')
  const parts: string[] = []
  if (d.os_hang)
    parts.push(
      t('widgets.sidebars.node.watchdogSection.osHang', {
        seconds: d.watchdog_timeout_sec,
      }),
    )
  if (d.agent_health)
    parts.push(t('widgets.sidebars.node.watchdogSection.agentHealth'))
  if (d.network)
    parts.push(
      t('widgets.sidebars.node.watchdogSection.network', {
        targets: (d.ping_targets || []).join(', '),
      }),
    )
  if (d.max_temperature_c)
    parts.push(
      t('widgets.sidebars.node.watchdogSection.temperature', {
        max: d.max_temperature_c,
      }),
    )
  if (d.max_load_1)
    parts.push(
      t('widgets.sidebars.node.watchdogSection.load', { max: d.max_load_1 }),
    )
  return parts.join(' · ') || t('widgets.sidebars.node.watchdogSection.armed')
}

// Terminal failure statuses of the last apply command. Anything else with a
// last_command present counts as still in flight.
const FAILED_STATUSES = new Set(['failed', 'expired', 'cancelled'])
const lastCommandFailed = (state?: WatchdogState): boolean =>
  !!state?.last_command && FAILED_STATUSES.has(state.last_command.status)

// Every state change goes through the confirm dialog first — arming and
// disarming both change whether a device reboots itself unattended.
type PendingAction =
  | { kind: 'arm'; preset: WatchdogPresetInfo; pingTarget?: string }
  | { kind: 'rearm'; config: WatchdogConfig }
  | { kind: 'arm-inherited'; config: WatchdogConfig; sourceLabel: string }
  | { kind: 'custom'; config: WatchdogConfig }
  | { kind: 'disarm' }

export const WatchdogSection: FC<{
  nodeId: string
  canManage: boolean
  online?: boolean
  agentVersion?: string
  // "Send to recovery" in the disarmed banner. The gating inputs (netboot
  // capability, boot-host match, the recovery action grant, whether the node is
  // already netbooted) all live in the node sidebar, so the decision is made
  // there and only the outcome is threaded down: absent callback = no button.
  onSendToRecovery?: () => void
}> = ({ nodeId, canManage, online = true, agentVersion, onSendToRecovery }) => {
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [target, setTarget] = useState('')
  const [pending, setPending] = useState<PendingAction | null>(null)
  const [editing, setEditing] = useState(false)

  const agentSupported = meetsMinAgentVersion(agentVersion)

  const queryClient = useQueryClient()
  const queryKey = ['node-watchdog', nodeId]

  const {
    data: state,
    isLoading,
    error: loadErr,
  } = useQuery({
    queryKey,
    queryFn: () => getWatchdog(nodeId),
    enabled: agentSupported,
    // Keep polling while the platform is waiting on the device: an apply that
    // is dispatched (or queued for an offline node) but not yet confirmed. A
    // terminally failed apply will not change without user action, so it does
    // not poll.
    refetchInterval: (query) => {
      const s = query.state.data
      if (!s) return false
      if (s.queued) return 5000
      if (s.desired?.enabled && !s.in_sync && !lastCommandFailed(s)) return 5000
      return false
    },
  })

  // The catalog is static per install, so it is cached hard: refetching it on
  // every panel open would add a request that can never return anything new.
  const { data: presets = [], error: presetsErr } = useQuery({
    queryKey: ['watchdog-presets', nodeId],
    queryFn: () => getWatchdogPresets(nodeId),
    enabled: agentSupported && canManage,
    staleTime: 60 * 60 * 1000,
  })

  const applyMutation = useMutation({
    mutationFn: (
      body: { preset: WatchdogPreset; ping_target?: string } | WatchdogConfig,
    ) => putWatchdog(nodeId, body),
    onSuccess: (next) => {
      queryClient.setQueryData(queryKey, next)
      setEditing(false)
      // A queued apply has NOT reached the device — the queued banner covers
      // it; only a live dispatch earns the optimistic notice.
      if (!next.queued) {
        setNotice(t('widgets.sidebars.node.watchdogSection.sentToDevice'))
      }
    },
    onError: (err) =>
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.watchdogSection.armError'),
        ),
      ),
    onSettled: () => setBusy(null),
  })

  const disarmMutation = useMutation({
    mutationFn: () => disarmWatchdog(nodeId),
    onSuccess: (next) => {
      queryClient.setQueryData(queryKey, next)
      if (!next.queued)
        setNotice(t('widgets.sidebars.node.watchdogSection.disarmSent'))
    },
    onError: (err) =>
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.watchdogSection.disarmError'),
        ),
      ),
    onSettled: () => setBusy(null),
  })

  const confirmPending = () => {
    if (!pending) return
    setError(null)
    setNotice(null)
    if (pending.kind === 'disarm') {
      setBusy('disarm')
      disarmMutation.mutate()
    } else if (pending.kind === 'rearm') {
      setBusy('rearm')
      applyMutation.mutate(pending.config)
    } else if (pending.kind === 'arm-inherited') {
      setBusy('arm-inherited')
      // Arming an inherited config stores it as the node's own desired state
      // (the LED pattern): the walk supplied the CONTENT, the arm is still an
      // explicit per-node action.
      applyMutation.mutate(pending.config)
    } else if (pending.kind === 'custom') {
      setBusy('custom')
      // Closing here unmounted the form — which holds its draft in local state —
      // before the server answered, so a rejected config lost every field the
      // operator had typed and re-opened seeded from the STORED config. It
      // closes on success instead.
      applyMutation.mutate(pending.config)
    } else {
      setBusy(pending.preset.name)
      applyMutation.mutate({
        preset: pending.preset.name,
        ping_target: pending.pingTarget,
      })
    }
    setPending(null)
  }

  // What the confirmation says the device will do. Every arm path answers it:
  // a custom configuration is not less dangerous than a preset, it is only less
  // describable, so it gets the plain statement rather than no statement.
  const pendingConsequence = (): string => {
    if (!pending) return ''
    switch (pending.kind) {
      case 'disarm':
        return t('widgets.sidebars.node.watchdogSection.disarmConsequence')
      case 'rearm':
        return t('widgets.sidebars.node.watchdogSection.rearmConsequence', {
          config: state ? describe(state) : '',
        })
      case 'custom':
        return t('widgets.sidebars.node.watchdogSection.customConsequence')
      case 'arm-inherited':
        return t('widgets.sidebars.node.watchdogSection.inheritedConsequence', {
          source: pending.sourceLabel,
          config: state ? describe(state) : '',
        })
      case 'arm':
        return t('widgets.sidebars.node.watchdogSection.armConsequence', {
          preset: pending.preset.label,
          consequence: consequenceFor(pending.preset, pending.pingTarget),
        })
    }
  }

  // An inherited desired config (OU/root/account policy) is a proposal, not a
  // node state: nothing on the device is armed until the operator applies it
  // here, so it must not render as "not confirmed yet" or enable Disarm.
  const inheritedOnly =
    !!state?.desired?.enabled &&
    !!state?.desired_source &&
    state.desired_source !== 'node'
  const armed = !!state?.desired?.enabled && !inheritedOnly
  const failed = lastCommandFailed(state)

  // Agent too old (or version unknown): the command would be refused anyway, so
  // say what to do about it instead of rendering arm buttons that cannot work.
  if (!agentSupported) {
    return (
      <div className="flex flex-col gap-4">
        <div className="rounded border border-neutral-11 bg-neutral-13 px-3 py-2 text-xs text-neutral-5">
          {t('widgets.sidebars.node.watchdogSection.requiresAgent', {
            version: MIN_AGENT_VERSION,
            reported: agentVersion?.trim()
              ? t('widgets.sidebars.node.watchdogSection.reportsSuffix', {
                  agentVersion,
                })
              : '',
          })}
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      {isLoading && (
        <div className="text-sm text-neutral-6">
          {t('widgets.sidebars.node.watchdogSection.loading')}
        </div>
      )}

      {!!loadErr && !state && (
        <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-sm text-red-6">
          {extractApiErrorMessage(
            loadErr,
            t('widgets.sidebars.node.watchdogSection.loadError'),
          )}
        </div>
      )}

      {state && (
        <div className="flex flex-col gap-2">
          <InfoRow
            label={t('widgets.sidebars.node.watchdogSection.configuredLabel')}
          >
            {describe(state)}
            {inheritedOnly && (
              <span className="text-neutral-6">
                {' '}
                {t('widgets.sidebars.node.watchdogSection.inheritedFrom', {
                  source:
                    watchdogSourceLabel(state.desired_source) ??
                    state.desired_source,
                })}
              </span>
            )}
          </InfoRow>
          <InfoRow
            label={t('widgets.sidebars.node.watchdogSection.onDeviceLabel')}
          >
            {state.disarm_reason ? (
              <span className="text-red-6">
                {t('widgets.sidebars.node.watchdogSection.disarmedByDevice')}
              </span>
            ) : state.queued ? (
              <span className="text-amber-6">
                {t('widgets.sidebars.node.watchdogSection.queuedOffline')}
              </span>
            ) : state.in_sync ? (
              <span className="text-green-5">
                {t('widgets.sidebars.node.watchdogSection.runningConfig')}
              </span>
            ) : inheritedOnly ? (
              t('widgets.sidebars.node.watchdogSection.notArmedValue')
            ) : armed ? (
              failed ? (
                <span className="text-red-6">
                  {t('widgets.sidebars.node.watchdogSection.applyFailed')}
                </span>
              ) : (
                <span className="text-yellow-5">
                  {t('widgets.sidebars.node.watchdogSection.notConfirmedYet')}
                </span>
              )
            ) : (
              t('widgets.sidebars.node.watchdogSection.notArmedValue')
            )}
          </InfoRow>
        </div>
      )}

      {state?.queued && (
        <div className="rounded border border-amber-6/40 bg-amber-6/10 px-3 py-2 text-xs text-amber-6">
          {t('widgets.sidebars.node.watchdogSection.queuedBanner')}
        </div>
      )}

      {state?.last_refusal && (
        <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-xs text-red-6">
          {t('widgets.sidebars.node.watchdogSection.refused', {
            reason: state.last_refusal,
          })}
          <span className="whitespace-nowrap">
            &nbsp;
            <InfoHint
              className="align-middle"
              text={t('widgets.sidebars.node.watchdogSection.refusedHint')}
            />
          </span>
        </div>
      )}

      {failed && !state?.last_refusal && (
        <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-xs text-red-6">
          {t('widgets.sidebars.node.watchdogSection.lastApplyFailed', {
            details: state?.last_command?.details
              ? `: ${state.last_command.details}`
              : '',
          })}
        </div>
      )}

      {state?.disarm_reason && (
        <div className="flex flex-col gap-2 rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-xs text-red-6">
          <div>
            {t('widgets.sidebars.node.watchdogSection.deviceDisarmed', {
              reason: state.disarm_reason,
            })}
          </div>
          <div className="flex flex-wrap gap-2">
            {canManage && state.desired?.enabled && (
              <Button
                variant="destructive"
                kind="secondary"
                className="self-start text-xs"
                disabled={!!busy || !online}
                title={
                  !online
                    ? t(
                        'widgets.sidebars.node.watchdogSection.nodeMustBeOnline',
                      )
                    : t('widgets.sidebars.node.watchdogSection.rearmTitle')
                }
                onClick={() => {
                  const config = state.desired
                  if (config) setPending({ kind: 'rearm', config })
                }}
              >
                {busy === 'rearm'
                  ? t('widgets.sidebars.node.watchdogSection.rearming')
                  : t('widgets.sidebars.node.watchdogSection.rearm')}
              </Button>
            )}
            {/* A disarmed breaker means the node is stable but unsupervised, and
                the reason it kept rebooting is still on its disk. Recovery is
                where that can be read. Operator-triggered only: the platform
                never reboots a node by itself, and the common cause — a bad
                config on a healthy OS — does not deserve recovery. */}
            {onSendToRecovery && (
              <Button
                variant="normal"
                kind="secondary"
                className="self-start text-xs"
                disabled={!!busy || !online}
                title={t('widgets.sidebars.node.watchdogSection.recoveryTitle')}
                onClick={onSendToRecovery}
              >
                {t('widgets.sidebars.node.watchdogSection.sendToRecovery')}
              </Button>
            )}
          </div>
        </div>
      )}

      {state?.recovery_limited && (
        <div className="rounded border border-amber-6/40 bg-amber-6/10 px-3 py-2 text-xs text-amber-6">
          {t('widgets.sidebars.node.watchdogSection.recoveryLimited')}
        </div>
      )}

      {!!state?.public_ping_targets?.length && (
        <div className="rounded border border-amber-6/40 bg-amber-6/10 px-3 py-2 text-xs text-amber-6">
          {t('widgets.sidebars.node.watchdogSection.publicPingWarning', {
            targets: state.public_ping_targets.join(', '),
          })}
        </div>
      )}

      {notice && !state?.queued && (
        <div className="text-xs text-lime-6">{notice}</div>
      )}
      {error && (
        <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-sm text-red-6">
          {error}
        </div>
      )}

      <WatchdogLiveStatus
        nodeId={nodeId}
        online={online}
        desiredHash={state?.desired_hash}
      />

      {canManage && !editing && inheritedOnly && state?.desired && (
        <div className="flex flex-col gap-1.5">
          <Button
            variant="normal"
            kind="secondary"
            className="self-start text-xs"
            disabled={!!busy || !online}
            title={
              !online
                ? t('widgets.sidebars.node.watchdogSection.nodeMustBeOnline')
                : undefined
            }
            onClick={() =>
              setPending({
                kind: 'arm-inherited',
                config: { ...state.desired!, enabled: true },
                sourceLabel:
                  watchdogSourceLabel(state.desired_source) ??
                  state.desired_source ??
                  t('widgets.sidebars.node.watchdogSection.thePolicy'),
              })
            }
          >
            {busy === 'arm-inherited'
              ? t('widgets.sidebars.node.watchdogSection.applying')
              : t('widgets.sidebars.node.watchdogSection.armInherited')}
          </Button>
          <div className="text-xxs text-neutral-6">
            {t('widgets.sidebars.node.watchdogSection.inheritedNote')}
          </div>
        </div>
      )}

      {canManage && !editing && (
        <div className="flex flex-col gap-3">
          <SectionDivider
            label={t('widgets.sidebars.node.watchdogSection.presetsTitle')}
          />
          {!!presetsErr && (
            <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-xs text-red-6">
              {extractApiErrorMessage(
                presetsErr,
                t('widgets.sidebars.node.watchdogSection.presetsError'),
              )}
            </div>
          )}
          {presets.map((preset) => (
            <div key={preset.name} className="flex flex-col gap-1.5">
              <div className="flex items-center gap-2">
                <Button
                  variant="normal"
                  kind="secondary"
                  className="text-xs"
                  disabled={
                    !!busy ||
                    !online ||
                    (preset.requires_ping_target && !target.trim())
                  }
                  title={
                    !online
                      ? t(
                          'widgets.sidebars.node.watchdogSection.nodeMustBeOnline',
                        )
                      : undefined
                  }
                  onClick={() =>
                    setPending({
                      kind: 'arm',
                      preset,
                      pingTarget: preset.requires_ping_target
                        ? target.trim()
                        : undefined,
                    })
                  }
                >
                  {busy === preset.name
                    ? t('widgets.sidebars.node.watchdogSection.applying')
                    : preset.label}
                </Button>
                {preset.requires_ping_target && (
                  <input
                    type="text"
                    value={target}
                    onChange={(e) => setTarget(e.target.value)}
                    placeholder={t(
                      'widgets.sidebars.node.watchdogSection.pingTargetPlaceholder',
                    )}
                    disabled={!!busy}
                    className="w-36 rounded border border-neutral-11 bg-neutral-13 px-2 py-1 font-mono text-xs text-neutral-2"
                  />
                )}
              </div>
              <div className="text-xxs text-neutral-6">
                {preset.description}
              </div>
            </div>
          ))}

          <Button
            variant="normal"
            kind="secondary"
            className="self-start text-xs"
            disabled={!!busy || !online}
            title={
              !online
                ? t('widgets.sidebars.node.watchdogSection.nodeMustBeOnline')
                : t('widgets.sidebars.node.watchdogSection.customTitle')
            }
            onClick={() => setEditing(true)}
          >
            {t('widgets.sidebars.node.watchdogSection.customButton')}
          </Button>

          <SectionDivider
            label={t('widgets.sidebars.node.watchdogSection.danger')}
            tone="danger"
          />
          <Button
            variant="destructive"
            kind="secondary"
            className="text-xs"
            disabled={!!busy || !armed}
            onClick={() => setPending({ kind: 'disarm' })}
            title={
              armed
                ? t('widgets.sidebars.node.watchdogSection.disarmTitleArmed')
                : t('widgets.sidebars.node.watchdogSection.disarmTitleNotArmed')
            }
            grow
          >
            {busy === 'disarm'
              ? t('widgets.sidebars.node.watchdogSection.disarming')
              : t('widgets.sidebars.node.watchdogSection.disarmButton')}
          </Button>
        </div>
      )}

      {canManage && editing && (
        <WatchdogForm
          initial={state?.desired}
          disabled={!online}
          busy={busy === 'custom'}
          onSubmit={(config) => setPending({ kind: 'custom', config })}
          onCancel={() => setEditing(false)}
        />
      )}

      {!canManage && (
        <div className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.watchdogSection.noPermission')}
        </div>
      )}

      <ConfirmDialog
        open={!!pending}
        title={
          pending?.kind === 'disarm'
            ? t('widgets.sidebars.node.watchdogSection.confirmDisarmTitle')
            : pending?.kind === 'rearm'
              ? t('widgets.sidebars.node.watchdogSection.confirmRearmTitle')
              : t('widgets.sidebars.node.watchdogSection.confirmArmTitle')
        }
        destructive
        message={pendingConsequence()}
        confirmText={
          pending?.kind === 'disarm'
            ? t('widgets.sidebars.node.watchdogSection.disarm')
            : t('widgets.sidebars.node.watchdogSection.arm')
        }
        onConfirm={confirmPending}
        onCancel={() => setPending(null)}
      />
    </div>
  )
}
