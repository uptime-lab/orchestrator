import clsx from 'clsx'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiNodes } from '@/entities/node/api'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

type BootloaderMismatchBannerProps = {
  nodeId: string
  online: boolean
  // Whether this install can bake/flash a signed EEPROM (eeprom_signer cap).
  canProvision: boolean
  // The boot host the device's EEPROM currently points at (empty = not set).
  /** True when the device names a DIFFERENT install (vs no netboot setup at
   * all). Deliberately a flag, not the hostname: see `reason` below. */
  pointsElsewhere?: boolean
  /**
   * When the bootloader was written to the device but has not taken effect yet
   * (the EEPROM is flashed at command time and read at BOOT time). Derived by
   * the panel from the command log, so it survives switching nodes, a reload
   * and a backend restart — as local component state, keying the panel on the
   * node id would make losing it routine: select another node and the flow
   * starts over.
   */
  stagedAt?: string
  /**
   * When a reboot for this node was dispatched and has not landed yet. Also
   * from the command log, for the same reason: the wait outlives the click.
   */
  rebootStartedAt?: string
}

// A reboot is the slowest thing this panel asks for and the only one with no
// progress of its own, so it says how long it has been waiting.
// Elapsed rather than a countdown: an estimate would be a guess about hardware
// this platform explicitly does not model, and a countdown that reaches zero
// while nothing has happened reads as a failure that has not occurred.
const useElapsed = (since?: string): string | null => {
  const { t } = useTranslation()
  const [label, setLabel] = useState<string | null>(null)
  useEffect(() => {
    if (!since) {
      setLabel(null)
      return
    }
    const startedAt = new Date(since).getTime()
    if (Number.isNaN(startedAt)) {
      setLabel(null)
      return
    }
    const tick = () => {
      const seconds = Math.max(0, Math.floor((Date.now() - startedAt) / 1000))
      setLabel(
        seconds < 60
          ? t('widgets.sidebars.node.bootloaderMismatchBanner.elapsedSeconds', {
              seconds,
            })
          : t('widgets.sidebars.node.bootloaderMismatchBanner.elapsedMinutes', {
              minutes: Math.floor(seconds / 60),
              seconds: String(seconds % 60).padStart(2, '0'),
            }),
      )
    }
    tick()
    const interval = setInterval(tick, 1000)
    return () => clearInterval(interval)
  }, [since, t])
  return label
}

const WarnIcon: FC<{ className?: string }> = ({ className }) => (
  <svg
    className={className}
    fill="none"
    stroke="currentColor"
    viewBox="0 0 24 24"
    xmlns="http://www.w3.org/2000/svg"
  >
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth={2}
      d="M12 9v2m0 4h.01M5.07 19h13.86a2 2 0 001.74-2.99l-6.93-12a2 2 0 00-3.48 0l-6.93 12A2 2 0 005.07 19z"
    />
  </svg>
)

// Shown on a Raspberry Pi node whose bootloader is not set up for network boot
// on THIS install (its EEPROM points at another server, or at nothing). Gives a
// one-click path: update the bootloader, then reboot to apply — after which the
// mismatch clears and recovery/network-boot work.
export const BootloaderMismatchBanner: FC<BootloaderMismatchBannerProps> = ({
  nodeId,
  online,
  canProvision,
  pointsElsewhere,
  stagedAt,
  rebootStartedAt,
}) => {
  const { t } = useTranslation()
  const [updating, setUpdating] = useState(false)
  // Optimistic: the command log takes a moment to refresh, and a button that
  // does nothing visible for a second gets pressed twice. `stagedAt` is the
  // durable answer and wins as soon as it arrives.
  const [justTriggered, setJustTriggered] = useState(false)
  const triggered = justTriggered || !!stagedAt
  const [rebooting, setRebooting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Only a reboot that follows a bootloader write is APPLYING one. The panel
  // passes any in-flight `reboot` command, and this banner renders on every Pi
  // whose EEPROM points elsewhere — so without the `triggered` guard, rebooting
  // such a node from the Actions menu (or sending it to recovery, which also
  // dispatches `reboot`) announced "Rebooting to apply the bootloader" beside a
  // still-offered "Update bootloader" button, having written nothing. The node
  // then comes back with the mismatch unchanged, which reads as a failed flash.
  const rebootingFor = useElapsed(triggered ? rebootStartedAt : undefined)
  // Both actions on this banner are irreversible and were one click each, while
  // the OTHER entry point for the identical bootloader write warns that an
  // interrupted flash can leave the device unbootable until recovered over USB.
  // This banner renders on the Overview tab of any mismatched Pi, so it is the
  // one a user meets first.
  const [confirm, setConfirm] = useState<null | 'update' | 'reboot'>(null)

  const handleUpdate = useCallback(async () => {
    setConfirm(null)
    setError(null)
    setUpdating(true)
    try {
      await apiNodes.provisionEEPROM(nodeId)
      setJustTriggered(true)
    } catch (err) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.bootloaderMismatchBanner.updateError'),
        ),
      )
    } finally {
      setUpdating(false)
    }
  }, [nodeId, t])

  const handleReboot = useCallback(async () => {
    setConfirm(null)
    setError(null)
    setRebooting(true)
    try {
      await apiNodes.rebootNode(nodeId)
    } catch (err) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.bootloaderMismatchBanner.rebootError'),
        ),
      )
    } finally {
      setRebooting(false)
    }
  }, [nodeId, t])

  // Neither host is named. The device's current one identifies ANOTHER
  // deployment, which is not this tenant's business, and this install's own
  // address is noise to someone whose only action is "press the button".
  const reason = pointsElsewhere
    ? t('widgets.sidebars.node.bootloaderMismatchBanner.reasonPointsElsewhere')
    : t('widgets.sidebars.node.bootloaderMismatchBanner.reasonNotSetUp')

  return (
    <div
      className={clsx(
        'flex flex-col gap-1.5 px-2 py-[6px] rounded-[3px] border',
        'bg-yellow-6/10 border-yellow-6/50 text-yellow-5',
      )}
    >
      <div className="flex items-center gap-2">
        <WarnIcon className="w-3 h-3 flex-shrink-0" />
        <div className="font-medium text-xs">
          {rebootingFor
            ? t('widgets.sidebars.node.bootloaderMismatchBanner.rebooting', {
                elapsed: rebootingFor,
              })
            : triggered
              ? t('widgets.sidebars.node.bootloaderMismatchBanner.written')
              : t(
                  'widgets.sidebars.node.bootloaderMismatchBanner.needsUpdate',
                  { reason },
                )}
        </div>
      </div>
      {error && <div className="text-xs text-red-5">{error}</div>}
      <div className="flex items-center gap-2">
        {!triggered && canProvision && (
          <Button
            variant="normal"
            kind="secondary"
            onClick={() => setConfirm('update')}
            disabled={updating || !online}
            className="text-xs h-auto py-[3px] px-2"
          >
            {updating
              ? t('widgets.sidebars.node.bootloaderMismatchBanner.updating')
              : t(
                  'widgets.sidebars.node.bootloaderMismatchBanner.updateBootloader',
                )}
          </Button>
        )}
        {!triggered && !canProvision && (
          <div className="text-xs opacity-80">
            {t(
              'widgets.sidebars.node.bootloaderMismatchBanner.signingUnavailable',
            )}
          </div>
        )}
        {/* No button while a reboot is already on its way: the node is going
            down, and a second one either does nothing or interrupts the first. */}
        {triggered && !rebootingFor && (
          <Button
            variant="normal"
            kind="secondary"
            onClick={() => setConfirm('reboot')}
            disabled={rebooting || !online}
            className="text-xs h-auto py-[3px] px-2"
          >
            {rebooting
              ? t(
                  'widgets.sidebars.node.bootloaderMismatchBanner.rebootingButton',
                )
              : t(
                  'widgets.sidebars.node.bootloaderMismatchBanner.rebootToApply',
                )}
          </Button>
        )}
      </div>

      <ConfirmDialog
        open={confirm !== null}
        title={
          confirm === 'reboot'
            ? t('widgets.sidebars.node.bootloaderMismatchBanner.rebootToApply')
            : t(
                'widgets.sidebars.node.bootloaderMismatchBanner.updateBootloader',
              )
        }
        destructive
        confirmText={
          confirm === 'reboot'
            ? t('widgets.sidebars.node.bootloaderMismatchBanner.confirmReboot')
            : t(
                'widgets.sidebars.node.bootloaderMismatchBanner.updateBootloader',
              )
        }
        busy={updating || rebooting}
        onCancel={() => setConfirm(null)}
        onConfirm={confirm === 'reboot' ? handleReboot : handleUpdate}
        message={
          confirm === 'reboot'
            ? t(
                'widgets.sidebars.node.bootloaderMismatchBanner.confirmRebootMessage',
              )
            : t(
                'widgets.sidebars.node.bootloaderMismatchBanner.confirmUpdateMessage',
              )
        }
      />
    </div>
  )
}
