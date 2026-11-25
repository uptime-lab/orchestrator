import clsx from 'clsx'
import type { TFunction } from 'i18next'
import { FC, useCallback } from 'react'
import { useTranslation } from 'react-i18next'

import { commandTitle } from '@/widgets/sidebars/node/commandHistory'

import type { NodeCommand } from '@/entities/node/api/types'

import {
  REBOOT_EXPECTED_MS,
  rebootToRecoveryColor,
  RECOVERY_REBOOT_EXPECTED_MS,
  useTimedProgress,
} from './rebootProgress'

type CommandNotificationProps = {
  command: NodeCommand
  // The reboot is a one-shot reboot-to-recovery: tint the timed bar from
  // orange to violet as the node approaches coming back netbooted.
  toRecovery?: boolean
  // Expected reboot round-trip; the caller picks it from the boot context
  // (into recovery / out of recovery / plain).
  expectedMs?: number
  onClick?: () => void
  onDismiss?: () => void
}

const ACTIVE_STATUSES = new Set(['registered', 'sent', 'in_progress'])

// The agent phrases its reboot detail as "Restart scheduled for N seconds"
// or "System will restart in N seconds" depending on the stage — both are
// noise at N=0; a real delay stays visible ("Restart initiated (in 30s)").
const rebootDetails = (t: TFunction, details?: string): string => {
  const delay = details?.match(/(?:scheduled for|restart in) (\d+) seconds?/)
  if (delay && Number(delay[1]) > 0) {
    return t('widgets.commandNotification.restartInitiatedDelay', {
      seconds: delay[1],
    })
  }
  return t('widgets.commandNotification.restartInitiated')
}

const getStatusColor = (status: NodeCommand['status']): string => {
  switch (status) {
    case 'in_progress':
      return 'bg-yellow-6/10 border-yellow-6/50 text-yellow-5'
    case 'success':
      return 'bg-green-6/10 border-green-6/50 text-green-5'
    case 'failed':
      return 'bg-red-6/10 border-red-6/50 text-red-5'
    case 'registered':
    case 'sent':
      return 'bg-blue-6/10 border-blue-6/50 text-blue-5'
    default:
      return 'bg-neutral-8/10 border-neutral-8/50 text-neutral-6'
  }
}

export const CommandNotification: FC<CommandNotificationProps> = ({
  command,
  toRecovery = false,
  expectedMs,
  onClick,
  onDismiss,
}) => {
  const { t } = useTranslation()

  // A reboot completes only when the node reconnects; the agent's own
  // progress number is meaningless here (it reports 100% immediately). Show
  // a fixed-duration expectation bar instead.
  const isRebootInFlight =
    command.slug === 'reboot' && ACTIVE_STATUSES.has(command.status)
  const rebootWindowMs =
    expectedMs ??
    (toRecovery ? RECOVERY_REBOOT_EXPECTED_MS : REBOOT_EXPECTED_MS)
  const rebootProgress = useTimedProgress(
    isRebootInFlight ? command.created_at : undefined,
    rebootWindowMs,
  )
  const rebootSecondsLeft = Math.max(
    0,
    Math.ceil((rebootWindowMs * (1 - rebootProgress)) / 1000),
  )
  const rebootBarColor = toRecovery
    ? rebootToRecoveryColor(rebootProgress)
    : undefined

  const handleClick = useCallback(() => {
    onClick?.()
  }, [onClick])

  const handleDismiss = useCallback(
    (e: React.MouseEvent) => {
      e.stopPropagation()
      onDismiss?.()
    },
    [onDismiss],
  )

  const statusColor = getStatusColor(command.status)
  const commandName = commandTitle(command)

  return (
    <div
      className={clsx(
        'text-sm rounded p-3 px-4 border cursor-pointer transition-colors',
        statusColor,
        onClick && 'hover:opacity-80',
      )}
      onClick={onClick ? handleClick : undefined}
    >
      {/* Command name and status on the same line */}
      <div className="flex items-center justify-between gap-4">
        <div className="font-medium truncate">{commandName}</div>
        <div className="flex items-center gap-2 flex-shrink-0">
          {isRebootInFlight ? (
            <span
              className="text-xs uppercase"
              style={{ color: rebootBarColor }}
            >
              {rebootSecondsLeft > 0
                ? t('widgets.commandNotification.rebooting', {
                    seconds: rebootSecondsLeft,
                  })
                : t('widgets.commandNotification.waitingForNode')}
            </span>
          ) : (
            <>
              <span className="text-xs uppercase">{command.status}</span>
              {command.progress > 0 && command.status === 'in_progress' && (
                <span className="text-xs">{command.progress}%</span>
              )}
            </>
          )}
          {onDismiss && (
            <button
              type="button"
              aria-label={t('shared.toast.dismiss')}
              onClick={handleDismiss}
              className="text-xs leading-none px-1 opacity-70 hover:opacity-100"
            >
              ✕
            </button>
          )}
        </div>
      </div>

      {/* Description below, full width. The agent's raw reboot detail
          ("Restart scheduled for 0 seconds") is noise — show a human line,
          keeping the delay only when there actually is one. */}
      {isRebootInFlight ? (
        <div className="text-xs mt-1 opacity-80 break-words">
          {rebootDetails(t, command.details)}
        </div>
      ) : (
        command.details && (
          <div className="text-xs mt-1 opacity-80 break-words">
            {command.details}
          </div>
        )
      )}

      {/* Progress bar: timed expectation for reboots, agent-reported otherwise */}
      {isRebootInFlight ? (
        <div className="mt-2 h-1 bg-neutral-12 rounded-full overflow-hidden">
          <div
            className={clsx(
              'h-full transition-all duration-1000 ease-linear',
              !rebootBarColor && 'bg-yellow-5',
            )}
            style={{
              width: `${Math.round(rebootProgress * 100)}%`,
              backgroundColor: rebootBarColor,
            }}
          />
        </div>
      ) : (
        command.status === 'in_progress' &&
        command.progress > 0 && (
          <div className="mt-2 h-1 bg-neutral-12 rounded-full overflow-hidden">
            <div
              className="h-full bg-yellow-5 transition-all duration-300"
              style={{ width: `${command.progress}%` }}
            />
          </div>
        )
      )}
    </div>
  )
}
