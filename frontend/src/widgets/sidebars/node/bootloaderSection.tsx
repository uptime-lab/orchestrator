import { FC, ReactNode, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiNodes } from '@/entities/node/api'
import type { BootOption } from '@/entities/node/api/getBootOptions'
import { BaseFirmwareMap } from '@/entities/node/api/getEEPROMBaseFirmware'
import { BootOrder } from '@/entities/node/api/types'
import { boardSoC } from '@/entities/node/board'
import { fetchNodeCommandsFx } from '@/entities/node/model'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { InfoHint } from '@/shared/ui/infoHint'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { InfoRow, SectionDivider } from './infoRow.tsx'

interface BootloaderSectionProps {
  nodeId: string
  /**
   * Whether the platform would accept a command for this node right now,
   * mirroring the server's dispatch guard (online or maintenance) rather than
   * the stricter "online" this panel used to ask for.
   */
  canDispatch: boolean
  // Whether the caller's role may flash the bootloader (node.eeprom_flash).
  // Enable, Update and Read are offered only then: the server refuses them
  // otherwise.
  canFlashEEPROM?: boolean
  bootloaderVersion?: string
  bootloaderTimestamp?: number // unix seconds
  bootOrder?: BootOrder // the device's CURRENT order, for display only
  cpuModel?: string // maps to the SoC family (cm4/cm5) of the signer base
  currentBootDevice?: string // "http" = netbooted
  // Whether the node runs the recovery image. There the agent applies an
  // update through the boot ROM, and the node reboots within seconds of the
  // write instead of waiting for a separate reboot.
  inRecovery?: boolean
  storageDevicePaths?: string[] // reported block devices, e.g. ["/dev/nvme0n1"]
  // Whether the device currently allows the platform to flash the EEPROM from
  // the running OS, and the agent's own wording for why not.
  eepromEnabled?: boolean
  eepromDescription?: string
  /**
   * When a bootloader write landed on the device but has not taken effect yet.
   * Derived by the panel from the command log, so the "reboot to apply" step
   * survives switching nodes and reloading — it was local state, which meant
   * looking at another node and coming back lost the flow and invited a second
   * flash of the same firmware.
   */
  stagedAt?: string
  /** When a reboot for this node was dispatched and has not landed yet. */
  rebootStartedAt?: string
}

const formatDate = (unixSeconds?: number) =>
  unixSeconds ? new Date(unixSeconds * 1000).toISOString().slice(0, 10) : null

// Small chip for one boot-order entry, matching the sidebar style.
const LightTag: FC<{ children: React.ReactNode }> = ({ children }) => (
  <div className="inline-flex items-center gap-1.5 px-2 py-1 rounded-[4px] border border-neutral-4/50 text-neutral-4">
    <span className="text-sm">{children}</span>
  </div>
)

// One group header for this sub-view: the shared SectionDivider, nothing else.
// The group's action used to sit at the right of the rule, where it was read as
// a caption rather than a control — an operator flashed a bootloader without
// realising "Read from device" beside it was clickable. Actions now sit in the
// body as ordinary buttons.
const SectionHeader: FC<{ label: ReactNode }> = ({ label }) => (
  <SectionDivider label={label} />
)

// Every action in this sub-view is the same compact secondary button, including
// the one inside the staged-flash banner, so a control never changes shape from
// one group to the next.
const SectionAction: FC<{
  onClick: () => void
  disabled?: boolean
  title?: string
  children: ReactNode
}> = ({ onClick, disabled, title, children }) => (
  <div className="flex">
    <Button
      variant="normal"
      kind="secondary"
      onClick={onClick}
      disabled={disabled}
      title={title}
      className="text-xs h-auto py-[3px] px-2"
    >
      {children}
    </Button>
  </div>
)

// Bootloader (EEPROM firmware) identity + update. The version comes from the
// ROM's HTTP-boot User-Agent, so it reflects the firmware actually running on
// the device as of its last boot. "Update" re-bakes the signed per-device
// EEPROM from the signer's latest firmware base, preserving the netboot
// identity (embedded pubkey, per-serial HTTP path, adoption token) and APPLYING
// the platform's boot order — it does not keep whatever the board arrived with.
export const BootloaderSection: FC<BootloaderSectionProps> = ({
  nodeId,
  canDispatch,
  canFlashEEPROM = true,
  bootloaderVersion,
  bootloaderTimestamp,
  bootOrder = [],
  cpuModel,
  currentBootDevice,
  inRecovery = false,
  storageDevicePaths = [],
  eepromEnabled,
  eepromDescription,
  stagedAt,
  rebootStartedAt,
}) => {
  const { t } = useTranslation()
  const [isEnableConfirmOpen, setIsEnableConfirmOpen] = useState(false)
  const [isEnabling, setIsEnabling] = useState(false)
  const [enableError, setEnableError] = useState<string | null>(null)
  const [isConfirmOpen, setIsConfirmOpen] = useState(false)
  const [isUpdating, setIsUpdating] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Optimistic only. `stagedAt` is the durable answer — derived from the
  // command log by the panel — and takes over as soon as it arrives; this just
  // covers the second before the list refreshes, so the button does not look
  // inert and get pressed twice.
  const [justFlashed, setJustFlashed] = useState(false)
  const flashTriggered = justFlashed || !!stagedAt
  const [rebooting, setRebooting] = useState(false)
  // The "Reboot to apply" step asks first. Without it the flash confirmation
  // was the only prompt, and it describes a write, not a restart: an operator
  // reading "written to the device" then pressed the button under it and the
  // node went to RESTARTING with nothing having said it would.
  const [isRebootConfirmOpen, setIsRebootConfirmOpen] = useState(false)
  const [baseFirmware, setBaseFirmware] = useState<BaseFirmwareMap | null>(null)
  // The hex digit of each boot device comes from the server's own catalog, so
  // the panel never keeps a second copy of a mapping the domain already owns.
  const [bootOptions, setBootOptions] = useState<BootOption[] | null>(null)

  useEffect(() => {
    let cancelled = false
    apiNodes
      .getEEPROMBaseFirmware()
      .then((fw) => {
        if (!cancelled) setBaseFirmware(fw)
      })
      .catch(() => {
        // No signer / unreachable: silently skip the freshness badge.
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    apiNodes
      .getBootOptions()
      .then((opts) => {
        if (!cancelled) setBootOptions(opts)
      })
      .catch(() => {
        // Catalog unreachable: the chips still render, the packed value does
        // not. Showing a value assembled from a guessed digit would be a claim
        // about the device's EEPROM that nothing here measured.
      })
    return () => {
      cancelled = true
    }
  }, [])

  // The ROM only runs the EEPROM recovery flash from SD/USB media, and a
  // managed OS flashes directly via flashrom. A purely netbooted device with
  // no local media cannot be flashed remotely at all — hide the action.
  const canFlash = useMemo(() => {
    if (currentBootDevice !== 'http') return true
    return storageDevicePaths.some(
      (p) => p.startsWith('/dev/mmcblk') || p.startsWith('/dev/sd'),
    )
  }, [currentBootDevice, storageDevicePaths])

  // No fallback SoC: an unrecognised board must yield NO badge rather than be
  // compared against CM5 firmware, which would show a false "update available"
  // (and invite a needless EEPROM flash) on hardware we cannot identify.
  const soc = boardSoC(cpuModel)
  const base = soc ? baseFirmware?.[soc] : undefined
  // The signer reports a SHORT hash (e.g. "f68405bc") while the agent reports
  // the FULL git hash (and older captures may carry a " (release)" suffix), so
  // compare by hash token + prefix, not exact string, or a current node reads
  // as "update available".
  const hashToken = (v?: string) =>
    (v ?? '').trim().split(/\s+/)[0].toLowerCase()
  const baseHash = hashToken(base?.version)
  const nodeHash = hashToken(bootloaderVersion)
  const freshness: 'current' | 'outdated' | null =
    baseHash && nodeHash
      ? nodeHash.startsWith(baseHash) || baseHash.startsWith(nodeHash)
        ? 'current'
        : 'outdated'
      : null
  // Display the short 8-hex form — the same length the signer base and the
  // historical ROM-UA capture use.
  const shortBootloader = nodeHash ? nodeHash.slice(0, 8) : bootloaderVersion

  const handleUpdate = async () => {
    setIsUpdating(true)
    setError(null)
    try {
      // Deliberately send NO boot order, so the backend bakes the platform's
      // own (NetworkFirstBootOrder: http -> nvme -> usb -> sd -> restart).
      //
      // This used to pass the device's CURRENT order back, which preserved
      // whatever the board arrived with — a second-hand or previously provisioned Pi
      // kept its stock SD-first order, never netbooted, and the panel's own
      // copy two lines below ("the boot order is managed by the platform")
      // was simply untrue for it. Preserving protects a hand-set order that no
      // UI can currently set, at the cost of the case operators actually hit.
      await apiNodes.provisionEEPROM(nodeId)
      await fetchNodeCommandsFx(nodeId)
      setJustFlashed(true)
      setIsConfirmOpen(false)
    } catch (err) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.bootloaderSection.updateError'),
        ),
      )
      setIsConfirmOpen(false)
    } finally {
      setIsUpdating(false)
    }
  }

  const handleApplyReboot = async () => {
    setError(null)
    setRebooting(true)
    try {
      await apiNodes.rebootNode(nodeId)
      setIsRebootConfirmOpen(false)
    } catch (err) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.bootloaderSection.rebootError'),
        ),
      )
      setIsRebootConfirmOpen(false)
    } finally {
      setRebooting(false)
    }
  }

  // Live EEPROM config dump: the actually-flashed rpi-eeprom-config, dispatched
  // as a read-only command and polled — real substance for this sub-view rather
  // than values inferred from node fields.
  const [cfgEntries, setCfgEntries] = useState<
    { key: string; value: string }[] | null
  >(null)
  const [cfgState, setCfgState] = useState<'idle' | 'scanning' | 'error'>(
    'idle',
  )
  const [cfgError, setCfgError] = useState<string | null>(null)
  // Cancels an in-flight EEPROM-config poll on unmount or node switch.
  const cfgActive = useRef(true)
  useEffect(() => {
    cfgActive.current = true
    return () => {
      cfgActive.current = false
    }
  }, [nodeId])
  const scanConfig = async () => {
    setCfgState('scanning')
    setCfgError(null)
    setCfgEntries(null)
    try {
      const commandId = await apiNodes.scanEEPROMConfig(nodeId)
      const deadline = Date.now() + 20000
      for (;;) {
        await new Promise((r) => setTimeout(r, 1500))
        if (!cfgActive.current) return
        const res = await apiNodes.getEEPROMConfigScan(nodeId, commandId)
        if (!cfgActive.current) return
        if (res.status === 'completed') {
          setCfgEntries(res.entries ?? [])
          setCfgState('idle')
          return
        }
        if (res.status === 'failed') {
          setCfgError(
            res.detail ??
              t('widgets.sidebars.node.bootloaderSection.scanIncomplete'),
          )
          setCfgState('error')
          return
        }
        if (Date.now() > deadline) {
          setCfgError(t('widgets.sidebars.node.bootloaderSection.scanTimeout'))
          setCfgState('error')
          return
        }
      }
    } catch (err) {
      if (!cfgActive.current) return
      setCfgError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.bootloaderSection.cfgReadError'),
        ),
      )
      setCfgState('error')
    }
  }

  const buildDate = formatDate(bootloaderTimestamp)

  // BOOT_ORDER packs the list into one number that the firmware reads right to
  // left, so the FIRST device to be tried is the LAST digit. Assembled only
  // when every listed device has a digit: a partial value would name a boot
  // order the device does not have.
  const bootDigits = useMemo(() => {
    const map = new Map<string, string>()
    for (const opt of bootOptions ?? []) map.set(opt.value, opt.hex_digit)
    return map
  }, [bootOptions])
  const bootOrderHex = useMemo(() => {
    if (bootOrder.length === 0) return null
    const digits = bootOrder.map((d) => bootDigits.get(d))
    if (digits.some((d) => !d)) return null
    return `0x${digits.reverse().join('')}`
  }, [bootOrder, bootDigits])

  return (
    <div className="flex flex-col gap-4">
      {/* Laid out as the sidebar's standard label/value table — the same
          SectionDivider + InfoRow primitives the Overview and Properties tabs
          use — so this sub-view reads as part of the panel instead of a stack
          of sentences. A group's action is a button in its body, never a word
          in its header, and every action here is read-only or confirmed. */}
      <div className="flex flex-col gap-2">
        <SectionHeader
          label={t('widgets.sidebars.node.bootloaderSection.firmware')}
        />

        <InfoRow
          label={
            <span className="inline-flex items-center gap-1.5">
              {t('widgets.sidebars.node.bootloaderSection.version')}
              <InfoHint
                text={t('widgets.sidebars.node.bootloaderSection.versionHint')}
              />
            </span>
          }
        >
          {bootloaderVersion ? (
            <div className="flex items-center gap-2 flex-wrap">
              <span title={bootloaderVersion}>{shortBootloader}</span>
              {buildDate && <span className="text-neutral-7">{buildDate}</span>}
              {freshness === 'current' && (
                <span className="text-[11px] px-1.5 py-0.5 rounded border border-lime-6/40 text-lime-6">
                  {t('widgets.sidebars.node.bootloaderSection.upToDate')}
                </span>
              )}
              {/* A flash that awaits its reboot preempts the freshness verdict:
                  the device still RUNS (and reports) the old version, so the
                  badge would read "update available" and invite the same flash
                  again — the operator's next step is a reboot. */}
              {freshness === 'outdated' && !flashTriggered && (
                <span className="text-[11px] px-1.5 py-0.5 rounded border border-yellow-6/40 text-yellow-6">
                  {t('widgets.sidebars.node.bootloaderSection.updateAvailable')}
                </span>
              )}
            </div>
          ) : (
            <span className="text-neutral-6">
              {t('widgets.sidebars.node.bootloaderSection.noVersion')}
            </span>
          )}
        </InfoRow>

        {freshness === 'outdated' && base && (
          <InfoRow
            label={t('widgets.sidebars.node.bootloaderSection.latestLabel')}
          >
            <div className="flex items-center gap-2 flex-wrap">
              <span>{base.version}</span>
              {formatDate(base.build_timestamp) && (
                <span className="text-neutral-7">
                  {formatDate(base.build_timestamp)}
                </span>
              )}
            </div>
          </InfoRow>
        )}

        {/* Whether the platform may write the chip from the running system at
            all: a prerequisite of the Update action right below it, which is
            why it reads as one more firmware fact rather than its own section.
            Three states, not two — an undefined flag is a node that has not
            reported its hardware yet, and calling that "disabled" states
            something about the device that nothing measured. The action is
            offered in the unknown case too: it is the only way to find out. */}
        <InfoRow
          label={
            <span className="inline-flex items-center gap-1.5">
              {t('widgets.sidebars.node.bootloaderSection.flashingFromOSLabel')}
              <InfoHint
                text={t(
                  'widgets.sidebars.node.bootloaderSection.flashingFromOSHint',
                )}
              />
            </span>
          }
        >
          {eepromEnabled === true ? (
            <span className="text-green-5">
              {t('widgets.sidebars.node.bootloaderSection.enabled')}
            </span>
          ) : !canFlashEEPROM ? (
            <span className="text-neutral-6">
              {t('widgets.sidebars.node.bootloaderSection.notEnabled')}
            </span>
          ) : (
            <SectionAction
              onClick={() => setIsEnableConfirmOpen(true)}
              disabled={!canDispatch || isEnabling}
              title={
                !canDispatch
                  ? t('widgets.sidebars.node.bootloaderSection.nodeUnavailable')
                  : eepromDescription
              }
            >
              {isEnabling
                ? t('widgets.sidebars.node.bootloaderSection.enabling')
                : t('widgets.sidebars.node.bootloaderSection.enable')}
            </SectionAction>
          )}
        </InfoRow>

        {enableError && (
          <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-xs text-red-6">
            {enableError}
          </div>
        )}

        {canFlashEEPROM && canFlash && !flashTriggered && (
          <SectionAction
            onClick={() => setIsConfirmOpen(true)}
            disabled={isUpdating || !canDispatch}
            title={
              canDispatch
                ? undefined
                : t('widgets.sidebars.node.bootloaderSection.nodeUnavailable')
            }
          >
            {isUpdating
              ? t('widgets.sidebars.node.bootloaderSection.updating')
              : t('widgets.sidebars.node.bootloaderSection.updateBootloader')}
          </SectionAction>
        )}

        {!canFlash && (
          <div className="text-xs text-neutral-6">
            {t('widgets.sidebars.node.bootloaderSection.updateUnavailable')}
            <span className="whitespace-nowrap">
              &nbsp;
              <InfoHint
                className="align-middle"
                text={t(
                  'widgets.sidebars.node.bootloaderSection.updateUnavailableHint',
                )}
              />
            </span>
          </div>
        )}

        {error && (
          <div className="text-xs text-red-6 bg-red-6/10 border border-red-6/20 rounded px-2 py-1.5">
            {error}
          </div>
        )}

        {/* Shown even while an error is up: with the Update action standing
            down for the staged flash, the Reboot button here is the only
            control left that can retry (and clear the error). Hiding it on
            error left the section with no actions at all. */}
        {flashTriggered && (
          <div className="flex flex-col gap-1.5 text-xs text-lime-6 bg-lime-6/10 border border-lime-6/20 rounded px-2 py-1.5">
            <div>
              {rebootStartedAt || inRecovery
                ? t('widgets.sidebars.node.bootloaderSection.rebootingToApply')
                : t(
                    'widgets.sidebars.node.bootloaderSection.writtenRebootToApply',
                  )}
            </div>
            {/* No button while a reboot is already on its way: the node is
                going down, and a second one either does nothing or interrupts
                the first. */}
            {!rebootStartedAt && !inRecovery && (
              <SectionAction
                onClick={() => setIsRebootConfirmOpen(true)}
                disabled={rebooting || !canDispatch}
              >
                {rebooting
                  ? t('widgets.sidebars.node.bootloaderSection.rebooting')
                  : t('widgets.sidebars.node.bootloaderSection.rebootToApply')}
              </SectionAction>
            )}
          </div>
        )}
      </div>

      {/* Boot order — read-only, and its own group because five chips do not
          fit a value column. The platform manages it (network boot first with a
          local-OS fallback) and applies it only through signed EEPROM packages;
          the Update action above re-bakes the EEPROM with the PLATFORM order. */}
      <div className="flex flex-col gap-2">
        <SectionHeader
          label={
            <span className="inline-flex items-center gap-1.5">
              {t('widgets.sidebars.node.bootloaderSection.bootOrder')}
              <InfoHint
                text={t(
                  'widgets.sidebars.node.bootloaderSection.bootOrderHint',
                )}
              />
            </span>
          }
        />
        {currentBootDevice && (
          <InfoRow
            label={t('widgets.sidebars.node.bootloaderSection.bootedFromLabel')}
          >
            {currentBootDevice}
          </InfoRow>
        )}
        {bootOrderHex && (
          <InfoRow
            label={t('widgets.sidebars.node.bootloaderSection.valueLabel')}
          >
            <span className="font-mono">{bootOrderHex}</span>
          </InfoRow>
        )}
        {bootOrder.length === 0 ? (
          <div className="text-sm text-neutral-6">
            {t('widgets.sidebars.node.bootloaderSection.noBootOrder')}
          </div>
        ) : (
          /* The decoding of that value: each chip carries the digit it
             contributes, in the order the firmware tries them. Read the chips
             backwards and they spell the number above. */
          <div className="flex items-center gap-2 flex-wrap">
            {bootOrder.map((device, index) => (
              <LightTag key={device}>
                <span className="text-xs font-semibold text-neutral-7 mr-1">
                  {index + 1}.
                </span>
                {device}
                {bootDigits.get(device) && (
                  <span className="ml-1.5 font-mono text-xs text-neutral-7">
                    {bootDigits.get(device)}
                  </span>
                )}
              </LightTag>
            ))}
          </div>
        )}
      </div>

      {/* Live EEPROM config: the actually-flashed rpi-eeprom-config. Disabled
          while netbooted: the scan runs `sudo rpi-eeprom-config`, and the
          recovery bootstrap has neither, so the click could only produce a
          failed command. */}
      <div className="flex flex-col gap-2">
        <SectionHeader
          label={
            <span className="inline-flex items-center gap-1.5">
              {t('widgets.sidebars.node.bootloaderSection.liveConfigTitle')}
              <InfoHint
                text={t(
                  'widgets.sidebars.node.bootloaderSection.liveConfigHint',
                )}
              />
            </span>
          }
        />
        {canFlashEEPROM && (
          <SectionAction
            onClick={() => void scanConfig()}
            disabled={
              cfgState === 'scanning' ||
              !canDispatch ||
              currentBootDevice === 'http'
            }
            title={
              currentBootDevice === 'http'
                ? t('widgets.sidebars.node.bootloaderSection.netbootScanTitle')
                : canDispatch
                  ? undefined
                  : t('widgets.sidebars.node.bootloaderSection.nodeUnavailable')
            }
          >
            {cfgState === 'scanning'
              ? t('widgets.sidebars.node.bootloaderSection.reading')
              : t('widgets.sidebars.node.bootloaderSection.readFromDevice')}
          </SectionAction>
        )}
        {cfgState === 'error' && (
          <div className="text-xs text-red-6">{cfgError}</div>
        )}
        {cfgEntries !== null && cfgEntries.length === 0 && (
          <div className="text-xs text-neutral-6">
            {t('widgets.sidebars.node.bootloaderSection.noConfigReturned')}
          </div>
        )}
        {cfgEntries && cfgEntries.length > 0 && (
          <div className="flex flex-col gap-0.5 rounded border border-neutral-4/30 p-2 font-mono text-[11px]">
            {cfgEntries.map((e) => (
              <div key={e.key} className="flex gap-2">
                <span className="text-neutral-6">{e.key}</span>
                <span className="text-neutral-2 break-all">{e.value}</span>
              </div>
            ))}
          </div>
        )}
      </div>

      <ConfirmDialog
        open={isEnableConfirmOpen}
        title={t('widgets.sidebars.node.bootloaderSection.enableConfirmTitle')}
        message={
          // Three cases, not two. socFromCPUModel returns null for any board it
          // does not recognise AND for a node whose system_info has not
          // arrived — and the old copy folded that into the reassuring branch,
          // telling the operator the node "does not reboot" on a board we
          // cannot identify — a claim nothing here can support. The
          // freshness badge in this same panel already refuses
          // to guess on an unknown SoC; this now matches it.
          soc === 'cm4' ? (
            <>
              {t('widgets.sidebars.node.bootloaderSection.enableCm4Message')}
              <span className="whitespace-nowrap">
                &nbsp;
                <InfoHint
                  className="align-middle"
                  text={t(
                    'widgets.sidebars.node.bootloaderSection.enableCm4Hint',
                  )}
                />
              </span>
            </>
          ) : soc === null ? (
            t('widgets.sidebars.node.bootloaderSection.enableUnknownMessage')
          ) : (
            t('widgets.sidebars.node.bootloaderSection.enableNoRebootMessage')
          )
        }
        confirmText={t('widgets.sidebars.node.bootloaderSection.enable')}
        destructive={soc === 'cm4' || soc === null}
        busy={isEnabling}
        onConfirm={async () => {
          setIsEnableConfirmOpen(false)
          setEnableError(null)
          setIsEnabling(true)
          try {
            await apiNodes.postNodeCommand(nodeId, {
              command: 'enable_eeprom',
              params: {},
            })
            await fetchNodeCommandsFx(nodeId)
          } catch (err) {
            setEnableError(
              extractApiErrorMessage(
                err,
                t('widgets.sidebars.node.bootloaderSection.enableError'),
              ),
            )
          } finally {
            setIsEnabling(false)
          }
        }}
        onCancel={() => setIsEnableConfirmOpen(false)}
      />

      <ConfirmDialog
        open={isConfirmOpen}
        title={t('widgets.sidebars.node.bootloaderSection.confirmUpdateTitle')}
        message={
          <>
            {inRecovery
              ? t(
                  'widgets.sidebars.node.bootloaderSection.confirmUpdateMessageRecovery',
                )
              : t(
                  'widgets.sidebars.node.bootloaderSection.confirmUpdateMessage',
                )}
            <span className="whitespace-nowrap">
              &nbsp;
              <InfoHint
                className="align-middle"
                text={t(
                  'widgets.sidebars.node.bootloaderSection.netbootIdentityKept',
                )}
              />
            </span>
          </>
        }
        confirmText={t('widgets.sidebars.node.bootloaderSection.update')}
        destructive
        busy={isUpdating}
        onConfirm={handleUpdate}
        onCancel={() => setIsConfirmOpen(false)}
      />

      <ConfirmDialog
        open={isRebootConfirmOpen}
        title={t('widgets.sidebars.node.bootloaderSection.rebootToApply')}
        message={t(
          'widgets.sidebars.node.bootloaderSection.rebootConfirmMessage',
        )}
        confirmText={t('widgets.sidebars.node.bootloaderSection.rebootConfirm')}
        destructive
        busy={rebooting}
        onConfirm={handleApplyReboot}
        onCancel={() => setIsRebootConfirmOpen(false)}
      />
    </div>
  )
}
