import { useQuery } from '@tanstack/react-query'
import type { TFunction } from 'i18next'
import { FC, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  getWatchdogStatusScan,
  scanWatchdogStatus,
  type WatchdogStatusScan,
} from '@/entities/node/api/watchdog'

import { Button } from '@/shared/ui/button/button.tsx'
import { InfoHint } from '@/shared/ui/infoHint'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { InfoRow, SectionDivider } from './infoRow.tsx'

// A live read of the device, next to the platform's memory of it.
//
// Everything in WatchdogState comes from the last converge command: the hash
// the device confirmed, the refusal it returned, the breaker report it pushed.
// None of that notices a `systemctl stop watchdog.service` typed on the node —
// the platform goes on saying "running this configuration" indefinitely. This
// panel asks the device instead, on demand: it is a dispatched command, so it
// is deliberately not automatic.

// The probe is a dispatched command, so the answer arrives asynchronously. Two
// seconds is fast enough to feel immediate for a read-only script and slow
// enough not to hammer the command table.
const POLL_MS = 2000
// A probe that never reaches a terminal state (the node dies mid-scan) would
// otherwise poll for as long as the panel stays open.
const POLL_DEADLINE_MS = 60_000

const describeDrift = (
  t: TFunction,
  scan: WatchdogStatusScan,
  desiredHash?: string,
): string | null => {
  // Only the device's OWN breaker is a fault. A disarm the platform asked for
  // leaves the same on-disk state, and reporting that back as "the breaker
  // fired" raised an alarm for the most ordinary action in the feature.
  if (scan.breaker_disarmed) {
    return t('widgets.sidebars.node.watchdogLiveStatus.breakerDisarmed', {
      reason: scan.breaker_reason ? `: ${scan.breaker_reason}` : '',
    })
  }
  if (scan.disarmed_by_platform) {
    return null
  }
  if (
    scan.service_state &&
    scan.service_state !== 'active' &&
    scan.breaker_known
  ) {
    // A breaker file with a stopped daemon is the case the platform cannot see:
    // the node was armed, and something on the device stopped it since.
    return t('widgets.sidebars.node.watchdogLiveStatus.daemonStopped')
  }
  if (desiredHash && scan.applied_hash && scan.applied_hash !== desiredHash) {
    return t('widgets.sidebars.node.watchdogLiveStatus.configDiffers')
  }
  return null
}

export const WatchdogLiveStatus: FC<{
  nodeId: string
  online?: boolean
  // The desired-state hash, so a device running an older configuration is
  // reported as drift rather than silently accepted.
  desiredHash?: string
}> = ({ nodeId, online = true, desiredHash }) => {
  const { t } = useTranslation()
  const [commandId, setCommandId] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [dispatching, setDispatching] = useState(false)
  // The poll below gives up at POLL_DEADLINE_MS, but stopping the poll alone
  // left the panel on "Probing…" forever: the last-seen status was still
  // "running" and nothing ever changed it. This flips when the deadline passes
  // without a terminal status, surfacing a timeout and re-enabling the button.
  const [timedOut, setTimedOut] = useState(false)

  const [startedAt, setStartedAt] = useState(0)

  const { data: scan, error: pollError } = useQuery({
    queryKey: ['watchdog-status-scan', nodeId, commandId],
    queryFn: () => getWatchdogStatusScan(nodeId, commandId as string),
    enabled: !!commandId,
    refetchInterval: (query) => {
      if (query.state.data?.status !== 'running') return false
      if (startedAt && Date.now() - startedAt > POLL_DEADLINE_MS) return false
      return POLL_MS
    },
  })

  // Arm the timeout alongside the poll, anchored to the same startedAt the
  // poll's own deadline uses; a terminal status before the deadline clears it.
  const scanStatus = scan?.status
  useEffect(() => {
    if (!commandId || scanStatus === 'completed' || scanStatus === 'failed') {
      return
    }
    const t = setTimeout(
      () => setTimedOut(true),
      Math.max(0, startedAt + POLL_DEADLINE_MS - Date.now()),
    )
    return () => clearTimeout(t)
  }, [commandId, scanStatus, startedAt])

  const probe = async () => {
    setError(null)
    setCommandId(null)
    setTimedOut(false)
    setDispatching(true)
    try {
      setStartedAt(Date.now())
      setCommandId(await scanWatchdogStatus(nodeId))
    } catch (err) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.watchdogLiveStatus.probeError'),
        ),
      )
    } finally {
      setDispatching(false)
    }
  }

  const probing = !timedOut && scan?.status === 'running'
  const drift =
    scan?.status === 'completed' ? describeDrift(t, scan, desiredHash) : null

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-3">
        <div className="flex-1 min-w-0">
          <SectionDivider
            label={t('widgets.sidebars.node.watchdogLiveStatus.title')}
          />
        </div>
        <InfoHint text={t('widgets.sidebars.node.watchdogLiveStatus.hint')} />
      </div>

      <Button
        variant="normal"
        kind="secondary"
        className="self-start text-xs"
        disabled={dispatching || !online || probing}
        title={
          !online
            ? t('widgets.sidebars.node.watchdogLiveStatus.nodeMustBeOnline')
            : t('widgets.sidebars.node.watchdogLiveStatus.probeTitle')
        }
        onClick={probe}
      >
        {dispatching || probing
          ? t('widgets.sidebars.node.watchdogLiveStatus.probing')
          : t('widgets.sidebars.node.watchdogLiveStatus.probeButton')}
      </Button>

      {timedOut &&
        scan?.status !== 'completed' &&
        scan?.status !== 'failed' && (
          <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-xs text-red-6">
            {t('widgets.sidebars.node.watchdogLiveStatus.timeoutNote')}
          </div>
        )}

      {(error || pollError) && (
        <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-xs text-red-6">
          {error ??
            extractApiErrorMessage(
              pollError,
              t('widgets.sidebars.node.watchdogLiveStatus.pollError'),
            )}
        </div>
      )}

      {scan?.status === 'failed' && (
        <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-xs text-red-6">
          {t('widgets.sidebars.node.watchdogLiveStatus.probeFailed', {
            detail: scan.detail ? `: ${scan.detail}` : '',
          })}
        </div>
      )}

      {scan?.status === 'completed' && (
        <div className="flex flex-col gap-2">
          {drift && (
            <div className="rounded border border-amber-6/40 bg-amber-6/10 px-3 py-2 text-xs text-amber-6">
              {drift}
            </div>
          )}
          <InfoRow
            label={t('widgets.sidebars.node.watchdogLiveStatus.daemonLabel')}
          >
            {scan.service_state ? (
              <span
                className={
                  scan.service_state === 'active'
                    ? 'text-green-5'
                    : 'text-neutral-5'
                }
              >
                {scan.service_state}
                {scan.service_enabled ? ` · ${scan.service_enabled}` : ''}
              </span>
            ) : (
              <span className="text-neutral-6">
                {t('widgets.sidebars.node.watchdogLiveStatus.couldNotTell')}
              </span>
            )}
          </InfoRow>
          <InfoRow label="/dev/watchdog">
            {scan.device_present ? (
              t('widgets.sidebars.node.watchdogLiveStatus.present')
            ) : (
              <span className="text-neutral-5">
                {t('widgets.sidebars.node.watchdogLiveStatus.absentNote')}
              </span>
            )}
          </InfoRow>
          <InfoRow
            label={t('widgets.sidebars.node.watchdogLiveStatus.filesLabel')}
          >
            {scan.conf_present
              ? 'watchdog.conf'
              : t('widgets.sidebars.node.watchdogLiveStatus.noConf')}
            {scan.drop_in_present
              ? t(
                  'widgets.sidebars.node.watchdogLiveStatus.breakerGateInstalled',
                )
              : t('widgets.sidebars.node.watchdogLiveStatus.noBreakerGate')}
          </InfoRow>
          <InfoRow
            label={t('widgets.sidebars.node.watchdogLiveStatus.breakerLabel')}
          >
            {scan.breaker_known ? (
              <>
                {scan.breaker_disarmed ? (
                  <span className="text-red-6">
                    {t('widgets.sidebars.node.watchdogLiveStatus.disarmed')}
                  </span>
                ) : scan.disarmed_by_platform ? (
                  <span className="text-neutral-5">
                    {t(
                      'widgets.sidebars.node.watchdogLiveStatus.offByPlatform',
                    )}
                  </span>
                ) : (
                  t('widgets.sidebars.node.watchdogLiveStatus.armed')
                )}
                {typeof scan.breaker_boots === 'number'
                  ? t('widgets.sidebars.node.watchdogLiveStatus.bootsCounted', {
                      boots: scan.breaker_boots,
                    })
                  : ''}
              </>
            ) : (
              <span className="text-neutral-6">
                {t('widgets.sidebars.node.watchdogLiveStatus.noBreakerState')}
              </span>
            )}
          </InfoRow>
          {typeof scan.liveness_age_sec === 'number' && (
            <InfoRow
              label={t(
                'widgets.sidebars.node.watchdogLiveStatus.livenessLabel',
              )}
            >
              {t('widgets.sidebars.node.watchdogLiveStatus.secondsAgo', {
                seconds: scan.liveness_age_sec,
              })}
            </InfoRow>
          )}
        </div>
      )}
    </div>
  )
}
