import { useNavigate, useSearch } from '@tanstack/react-router'
import clsx from 'clsx'
import {
  FC,
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import {
  rebootToRecoveryBadgeStyle,
  RECOVERY_REBOOT_EXPECTED_MS,
  useTimedProgress,
} from '@/widgets/commandNotification/rebootProgress.ts'
import { SidebarCaptureImage } from '@/widgets/sidebars/captureImage'
import { SidebarInstallAgent } from '@/widgets/sidebars/installAgent'
import { SidebarInstallOS } from '@/widgets/sidebars/installOS'
import { SidebarProvisionConnection } from '@/widgets/sidebars/provisionConnection'
import { SidebarRunPlaybook } from '@/widgets/sidebars/runPlaybook'
import { SidebarEnum } from '@/widgets/sidebars/types'

import { identifyNode } from '@/entities/deviceProfile'
import { deviceCapReasonText } from '@/entities/entitlement/deviceCapText'
import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import {
  NodeStatusReadyForAdoption,
  StorageDevice,
  type NodeCommand,
  type TrackedFile,
} from '@/entities/node/api/types'
import { useNode, useNodeCommands } from '@/entities/node/hooks.ts'
import {
  formatBlink,
  identifyBlinkStarted,
} from '@/entities/node/identifyBlink'
import { fetchNodeCommandsFx } from '@/entities/node/model'
import { powerSummary } from '@/entities/node/power.ts'
import { NodeStatusBadge } from '@/entities/node/ui/nodeStatusBadge.tsx'
import {
  isNetbootRecovery,
  isNodeDispatchable,
  isNodeHeldForWork,
  isNodeInConnectedState,
  nodeDisplayName,
} from '@/entities/node/utils.ts'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { useServerCapabilities } from '@/entities/serverCapabilities'
import { usePreferHostnames } from '@/entities/user/preferences'

import { IconDisk } from '@/shared/assets/icons'
import { uptimeLabel, useUptime } from '@/shared/hooks/useUptime'
import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { SourceOfferNote } from '@/shared/ui/sourceOfferNote'
import { showError, showSuccess } from '@/shared/ui/toast'
import { useCopyText } from '@/shared/utils/copyText'

import { SidebarProps } from '../types'
import { ActionConfirm } from './actionConfirm.tsx'
import { ActionRow } from './actionRow.tsx'
import { AgentVersionBadge } from './agentVersionBadge.tsx'
import { BootloaderMismatchBanner } from './bootloaderMismatchBanner.tsx'
import { BootloaderSection } from './bootloaderSection.tsx'
import { CommandHistory, commandTitle } from './commandHistory.tsx'
import { DevicePlatformSection } from './devicePlatformSection.tsx'
import { HostnameControl } from './hostnameControl.tsx'
import { InfoRow, SectionDivider } from './infoRow.tsx'
import { LEDSection } from './ledSection.tsx'
import { MetricsPanel } from './metricsPanel.tsx'
import { NodeAccessSection } from './nodeAccessSection.tsx'
import { NodeAttributesSection } from './nodeAttributesSection.tsx'
import { NodeFileEditor } from './nodeFileEditor.tsx'
import { NodeFilesSection } from './nodeFilesSection.tsx'
import { NodeNameControl } from './nodeNameControl.tsx'
import { NodeNotesSection } from './nodeNotesSection.tsx'
import { NodeOUSection } from './nodeOUSection.tsx'
import { NodeSecretsSection } from './nodeSecretsSection.tsx'
import { NodeUnitRow } from './nodeUnitRow.tsx'
import { NodeUtilitiesSection } from './nodeUtilitiesSection.tsx'
import { OverclockControl } from './overclockControl.tsx'
import { nodeIsBusy } from './overclockState'
import {
  buildStorageLines,
  cpuCoreName,
  deviceModelLabel,
  formatAgo,
  formatFreq,
  shortBootloaderVersion,
  stockMaxFreqMHz,
  usbDeviceLine,
  useBootloaderFreshness,
  useOverclock,
} from './overviewData.ts'
import { PlatformRow } from './platformRow.tsx'
import { ReplaceNodeControl } from './replaceNodeControl.tsx'
import { StorageDeviceSection } from './storageDeviceSection.tsx'
import { SubViewHeader } from './subViewHeader.tsx'
import { TerminalPanel } from './terminalPanel.tsx'
import { WatchdogSection } from './watchdogSection.tsx'

const getStatusConfig = (
  status: NodeCommand['status'],
): {
  bgColor: string
  textColor: string
  borderColor: string
  icon: React.ReactNode
} => {
  switch (status) {
    case 'success':
      return {
        bgColor: 'bg-lime-6/10',
        textColor: 'text-lime-6',
        borderColor: 'border-lime-6/30',
        icon: (
          <svg className="w-3 h-3" fill="currentColor" viewBox="0 0 20 20">
            <path
              fillRule="evenodd"
              d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z"
              clipRule="evenodd"
            />
          </svg>
        ),
      }
    case 'failed':
      return {
        bgColor: 'bg-red-6/10',
        textColor: 'text-red-6',
        borderColor: 'border-red-6/30',
        icon: (
          <svg className="w-3 h-3" fill="currentColor" viewBox="0 0 20 20">
            <path
              fillRule="evenodd"
              d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z"
              clipRule="evenodd"
            />
          </svg>
        ),
      }
    case 'in_progress':
    case 'registered':
    case 'sent':
      return {
        bgColor: 'bg-yellow-6/10',
        textColor: 'text-yellow-6',
        borderColor: 'border-yellow-6/30',
        icon: (
          <svg className="w-3 h-3" fill="currentColor" viewBox="0 0 20 20">
            <path
              fillRule="evenodd"
              d="M10 18a8 8 0 100-16 8 8 0 000 16zm1-12a1 1 0 10-2 0v4a1 1 0 00.293.707l2.828 2.829a1 1 0 101.415-1.415L11 9.586V6z"
              clipRule="evenodd"
            />
          </svg>
        ),
      }
    default:
      return {
        bgColor: 'bg-neutral-9/10',
        textColor: 'text-neutral-6',
        borderColor: 'border-neutral-9/40',
        icon: (
          <svg className="w-3 h-3" fill="currentColor" viewBox="0 0 20 20">
            <path
              fillRule="evenodd"
              d="M10 18a8 8 0 100-16 8 8 0 000 16zM7 9a1 1 0 000 2h6a1 1 0 100-2H7z"
              clipRule="evenodd"
            />
          </svg>
        ),
      }
  }
}

// CommandChip is the compact command-status indicator that lives on the right
// of the identity row in the header (not a full-width bar in the content), so a
// command appearing/finishing never shifts the tab content. Clicking it opens
// the command history; ✕ dismisses.
const CommandChip: FC<{
  command: NodeCommand
  onClick: () => void
  onDismiss: () => void
}> = ({ command, onClick, onDismiss }) => {
  const { t } = useTranslation()
  const cfg = getStatusConfig(command.status)
  return (
    <div
      className={clsx(
        'flex items-center gap-1.5 h-6 px-2 rounded border flex-shrink-0 max-w-[55%]',
        cfg.bgColor,
        cfg.textColor,
        cfg.borderColor,
      )}
    >
      <span className="flex-shrink-0">{cfg.icon}</span>
      <button
        type="button"
        onClick={onClick}
        title={t('widgets.sidebars.node.panel.commandChip.viewHistory')}
        className="text-xxs font-medium uppercase tracking-wide truncate"
      >
        {commandTitle(command)}
      </button>
      <button
        type="button"
        aria-label={t('widgets.sidebars.node.panel.commandChip.dismiss')}
        onClick={onDismiss}
        className="flex-shrink-0 text-xs leading-none opacity-70 hover:opacity-100"
      >
        ✕
      </button>
    </div>
  )
}

// StorageDeviceListItem is one row of the Advanced → Storage list; clicking it
// opens the drive's details as a nested step of that sub-view.
const StorageDeviceListItem: FC<{
  devicePath: string
  deviceInfo: StorageDevice
  onSelect: (devicePath: string) => void
}> = ({ devicePath, deviceInfo, onSelect }) => {
  const { t } = useTranslation()
  const na = t('widgets.sidebars.node.panel.na')
  const displayLabel = deviceInfo.label || devicePath
  const capacityGB = deviceInfo.total_capacity
    ? (deviceInfo.total_capacity / (1024 * 1024 * 1024)).toFixed(1)
    : na

  // Calculate total used/available from partitions
  const totalUsed =
    deviceInfo.partitions?.reduce((sum, p) => sum + (p.used_space || 0), 0) || 0
  const totalAvailable =
    deviceInfo.partitions?.reduce(
      (sum, p) => sum + (p.available_space || 0),
      0,
    ) || 0
  const usedGB =
    totalUsed > 0 ? (totalUsed / (1024 * 1024 * 1024)).toFixed(1) : na
  const availableGB =
    totalAvailable > 0 ? (totalAvailable / (1024 * 1024 * 1024)).toFixed(1) : na

  // Build summary string
  const summaryText =
    totalUsed > 0
      ? t('widgets.sidebars.node.panel.storageItem.summaryWithUsage', {
          devicePath,
          label: displayLabel,
          capacity: capacityGB,
          used: usedGB,
          available: availableGB,
        })
      : t('widgets.sidebars.node.panel.storageItem.summary', {
          devicePath,
          label: displayLabel,
          capacity: capacityGB,
        })

  return (
    <button
      type="button"
      onClick={() => onSelect(devicePath)}
      className="flex items-center gap-2 text-left hover:text-neutral-2 hover:bg-neutral-13 rounded px-2 py-1 transition-colors text-sm text-neutral-4"
    >
      <IconDisk className="w-4 h-4 text-neutral-7 flex-shrink-0" />
      <span className="text-xs text-neutral-4 truncate">{summaryText}</span>
    </button>
  )
}

// Minimal stroke icons for the action rows (kept local to avoid a shared sprite).
const stroke = {
  className: 'w-4 h-4',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  viewBox: '0 0 24 24',
} as const
const IconReboot = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M4 12a8 8 0 018-8 8 8 0 016.9 4M20 4v4h-4M20 12a8 8 0 01-8 8 8 8 0 01-6.9-4M4 20v-4h4"
    />
  </svg>
)
const IconPower = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M12 3v9M6.5 7a8 8 0 1011 0"
    />
  </svg>
)
const IconOS = () => (
  <svg {...stroke}>
    <rect x="3" y="4" width="18" height="12" rx="1.5" />
    <path strokeLinecap="round" d="M8 20h8M12 16v4" />
  </svg>
)
const IconAgent = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M12 16V4m0 0l-4 4m4-4l4 4M5 16a4 4 0 004 4h6a4 4 0 004-4"
    />
  </svg>
)
// Backup: a disk (cylinder) with a down arrow — snapshot the disk into an image.
const IconBackup = () => (
  <svg {...stroke}>
    <ellipse cx="12" cy="5" rx="7" ry="2.5" />
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M5 5v6c0 1.4 3.1 2.5 7 2.5M19 5v5M15.5 17.5l2.5 2.5 2.5-2.5M18 14v6"
    />
  </svg>
)
// Restore: a disk with a counter-clockwise arrow — write an image back to disk.
const IconRestore = () => (
  <svg {...stroke}>
    <ellipse cx="12" cy="5" rx="7" ry="2.5" />
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M5 5v6c0 1.4 3.1 2.5 7 2.5M19 5v6M20 18a3 3 0 10-1 2.2M20 15v3h-3"
    />
  </svg>
)
const IconLink = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M9 15l6-6M10 6l1-1a4 4 0 016 6l-1 1M14 18l-1 1a4 4 0 01-6-6l1-1"
    />
  </svg>
)
const IconPlaybook = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M5 4h11l3 3v13H5zM16 4v3h3M9 12l3 2-3 2z"
    />
  </svg>
)
const IconTerminal = () => (
  <svg {...stroke}>
    <rect x="3" y="4" width="18" height="16" rx="1.5" />
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M7 9l3 3-3 3M13 15h4"
    />
  </svg>
)
const IconReplace = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M8 7h12m0 0l-4-4m4 4l-4 4M16 17H4m0 0l4 4m-4-4l4-4"
    />
  </svg>
)
const IconHostname = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M7 7h.01M7 3h5a2 2 0 011.4.6l7 7a2 2 0 010 2.8l-5.6 5.6a2 2 0 01-2.8 0l-7-7A2 2 0 015 8.6V5a2 2 0 012-2z"
    />
  </svg>
)
const IconHistory = () => (
  <svg {...stroke}>
    <circle cx="12" cy="12" r="9" />
    <path strokeLinecap="round" strokeLinejoin="round" d="M12 7v5l3 2" />
  </svg>
)
const IconDownload = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M12 3v12m0 0l-4-4m4 4l4-4M5 21h14"
    />
  </svg>
)
const IconTrash = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M4 7h16M9 7V5a1 1 0 011-1h4a1 1 0 011 1v2m-8 0v12a1 1 0 001 1h6a1 1 0 001-1V7"
    />
  </svg>
)
// Credentials: a key. Secrets: a padlock. Attributes: a tag.
const IconKey = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M15.75 5.25a3 3 0 013 3m3 0a6 6 0 01-7.029 5.912c-.563-.097-1.159.026-1.563.43L10.5 17.25H8.25v2.25H6v2.25H2.25v-2.818c0-.597.237-1.17.659-1.591l6.499-6.499c.404-.404.527-1 .43-1.563A6 6 0 1121.75 8.25z"
    />
  </svg>
)
const IconLock = () => (
  <svg {...stroke}>
    <rect x="4" y="10" width="16" height="10" rx="2" />
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M8 10V7a4 4 0 018 0v3"
    />
  </svg>
)
const IconTag = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M3 12l7-7h7a1 1 0 011 1v7l-7 7a1 1 0 01-1.4 0L3 13.4A1 1 0 013 12z"
    />
    <circle cx="14" cy="8" r="1.2" />
  </svg>
)
// Utilities: a wrench.
const IconWrench = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M14.7 6.3a4.5 4.5 0 00-6 6L3 18a2.1 2.1 0 003 3l5.7-5.7a4.5 4.5 0 006-6l-3 3-2.3-.7-.7-2.3 3-3z"
    />
  </svg>
)
// Advanced rows: watchdog (shield+heartbeat), files (document), storage (disk),
// boot order (ordered list), bootloader (chip).
const IconWatchdog = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M12 3l7 3v5c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6l7-3z"
    />
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M8.5 12h2l1-2 1.5 4 1-2h1.5"
    />
  </svg>
)
const IconFile = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M6 3h8l4 4v14H6zM14 3v4h4M9 13h6M9 17h6"
    />
  </svg>
)
const IconStorage = () => (
  <svg {...stroke}>
    <ellipse cx="12" cy="6" rx="7" ry="2.5" />
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M5 6v12c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5V6M5 12c0 1.4 3.1 2.5 7 2.5s7-1.1 7-2.5"
    />
  </svg>
)
const IconChip = () => (
  <svg {...stroke}>
    <rect x="7" y="7" width="10" height="10" rx="1.5" />
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M10 3v2M14 3v2M10 19v2M14 19v2M3 10h2M3 14h2M19 10h2M19 14h2"
    />
  </svg>
)
// Overclock: a speedometer (gauge with a needle pushed right).
const IconGauge = () => (
  <svg {...stroke}>
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M4 16a8 8 0 1116 0M12 16l4-5"
    />
    <path strokeLinecap="round" d="M4.5 19.5h15" />
  </svg>
)

// CopyButton copies a read-only value (IP/MAC/serial/bootloader) to the
// clipboard, sitting on the right of its row — same column and style as the
// Hostname "Change" control.
const CopyButton: FC<{ value: string; label: string }> = ({ value, label }) => {
  const { t } = useTranslation()
  const { state: copyState, copy } = useCopyText(1200)
  return (
    <Button
      size="sm"
      kind="secondary"
      title={t('widgets.sidebars.node.panel.copyButton.title', { label })}
      onClick={() => {
        void copy(value)
      }}
      className="flex-shrink-0"
    >
      {copyState === 'copied'
        ? t('widgets.sidebars.node.panel.copyButton.copied')
        : copyState === 'failed'
          ? t('shared.copy.manual')
          : t('widgets.sidebars.node.panel.copyButton.copy')}
    </Button>
  )
}

// Command-banner dismissals, kept at module scope so they persist across the
// node sidebar unmounting/remounting (navigating away and back). Command ids are
// UUIDs, so this is safe to share across nodes; it lives for the session.
const dismissedCommandIdsCache = new Set<string>()

// The sub-views that open inline inside the Actions tab. 'installOS' and
// 'restore' share the same picker (grouped images) with a different heading;
// Delete is one click and its consequence reaches beyond the platform: the row
// goes and the DEVICE is told nothing, so it keeps retrying with an identity
// nothing recognises. A network-first one cannot even reach recovery — it boots
// its installed OS and retries into nothing, powered and on the network but
// invisible.
//
// So the dialog says what deleting MEANS rather than restating the button, and
// names the route back. Both confirmations (the slide-in and the modal) use this
// one string: two wordings for the same irreversible action is how one of them
// ends up honest and the other does not.
//
// The copy lives at widgets.sidebars.node.panel.deleteWarning.

// A CONNECTED node is the case the platform refuses without an explicit force,
// because it is the one moment the device is still reachable. Confirming THIS
// wording is that explicit force: the point of the gate is that nobody deletes a
// live device without being told what happens to it, not that a flag exists.
//
// "Connected" is online OR maintenance OR restarting — see
// isNodeInConnectedState. A node wedged mid-install is in one of the latter two
// with a live stream, so saying "ONLINE" here would have contradicted the status
// shown three lines away in the same panel.
//
// The copy lives at widgets.sidebars.node.panel.deleteOnlineWarning.

// the 'confirm*' entries are slide-in confirmations instead of modal popups.
type ActionView =
  | 'installOS'
  | 'restore'
  | 'captureImage'
  | 'runPlaybook'
  | 'installAgent'
  | 'provisionConnection'
  | 'terminal'
  | 'hostname'
  | 'history'
  | 'replace'
  | 'confirmReboot'
  | 'confirmShutdown'
  | 'confirmRecovery'
  | 'confirmDelete'
  | 'credentials'
  | 'secrets'
  | 'attributes'
  | 'utilities'
  | 'leds'
  | 'platform'
  | 'watchdog'
  | 'overclock'
  | 'files'
  | 'storage'
  | 'bootloader'
  | 'readopt'

// Sub-views open as a right slide-in inside a specific tab (Actions / Properties
// / Advanced). openActionView routes to the hosting tab first so deep opens land
// on the right tab.
const PROPERTIES_VIEWS: ActionView[] = [
  'credentials',
  'secrets',
  'attributes',
  'utilities',
  'leds',
  'platform',
]
const ADVANCED_VIEWS: ActionView[] = [
  'watchdog',
  'overclock',
  'files',
  'storage',
  'bootloader',
  'readopt',
]
const viewHostTab = (v: ActionView): 'actions' | 'properties' | 'advanced' =>
  PROPERTIES_VIEWS.includes(v)
    ? 'properties'
    : ADVANCED_VIEWS.includes(v)
      ? 'advanced'
      : 'actions'

export const SidebarNode: FC<SidebarProps & { nodeId: string }> = ({
  nodeId,
  onClose,
}) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = useSearch({ from: '/_mainLayout' })
  const { node } = useNode({ nodeId })
  const check = useCan()
  const caps = useServerCapabilities()
  // The device's EEPROM points at a different install's boot server: netboot
  // recovery here would never complete (the ROM fetches from the other host),
  // so the platform refuses it (409 recovery_unreachable) and we surface a badge + disable the row.
  //
  // The other install's hostname is deliberately NOT shown. It is another
  // deployment's identity, and a tenant looking at their own node has no
  // business learning where the hardware used to live — "wrong, update it" is
  // the whole actionable content anyway.
  const bootHost = node?.system_info?.hardware?.boot?.boot_http_host
  const bootHostMismatch =
    !!caps.boot_host &&
    !!bootHost &&
    bootHost.toLowerCase() !== caps.boot_host.toLowerCase()
  const isFavorite = nodesModel.useFavoriteNodeIds().has(nodeId)
  const preferHostnames = usePreferHostnames()
  const commands = useNodeCommands(nodeId)
  const [isProcessing, setIsProcessing] = useState(false)
  const [preparingSD, setPreparingSD] = useState(false)
  const [identifyBusy, setIdentifyBusy] = useState(false)
  // The config file opened for editing inside Advanced → Config files, and the
  // drive opened inside Advanced → Storage. Both are nested steps of their
  // sub-view (list → detail), not panel takeovers.
  const [editingFile, setEditingFile] = useState<TrackedFile | null>(null)
  const [storageDevicePath, setStorageDevicePath] = useState<string | null>(
    null,
  )
  const [pendingConfirm, setPendingConfirm] = useState<
    'delete' | 'reject' | null
  >(null)

  // Actions-tab sub-view: which action form is open inline (slides in from the
  // right over the action list) instead of replacing the whole node panel.
  const [actionView, setActionView] = useState<ActionView | null>(null)
  // One-shot mount flag so the sub-view animates in each time it opens.
  const [subEnter, setSubEnter] = useState(false)

  // Calculate uptime using the hook
  const uptime = useUptime({
    bootedAt: node?.system_info?.hardware?.boot?.booted_at,
    lastSeenAt: node?.last_seen_at,
    status: node?.status || 'offline',
  })

  // Overclock detection reads the node's tracked config.txt (Raspberry Pi only).
  // Called unconditionally to satisfy the rules of hooks; it no-ops until the
  // node loads and reports a Raspberry Pi CPU.
  const overclock = useOverclock(
    nodeId,
    (node?.system_info?.hardware?.cpu?.model || '')
      .toLowerCase()
      .includes('raspberry'),
  )

  // Bootloader freshness vs the signer's latest base firmware (RPi + signer
  // only; null elsewhere). Drives the "up to date" badge on the bootloader row.
  const bootloaderFreshness = useBootloaderFreshness(
    node?.system_info?.hardware?.cpu?.model,
    node?.bootloader_version,
  )

  // Get activeTab from URL search params, default to 'overview'
  // History is reachable via the clock icon by the status, not the tab bar
  // (keeps the bar from overflowing), but stays a valid view.
  const NODE_TABS = ['overview', 'actions', 'properties', 'advanced'] as const
  type NodeTab = (typeof NODE_TABS)[number] | 'history'
  // Rendered tab captions; the NODE_TABS values stay URL/state identifiers.
  const tabLabels: Record<(typeof NODE_TABS)[number], string> = {
    overview: t('widgets.sidebars.node.panel.tabs.overview'),
    actions: t('widgets.sidebars.node.panel.tabs.actions'),
    properties: t('widgets.sidebars.node.panel.tabs.properties'),
    advanced: t('widgets.sidebars.node.panel.tabs.advanced'),
  }
  const NODE_VIEWS: readonly string[] = [...NODE_TABS, 'history']

  // Permission-driven visibility: hide tabs whose actions the user cannot
  // perform, so a viewer/manager sees a clean read-only view instead of
  // buttons that only 403. Backend enforces the same; this is UX.
  const ou = node?.ou_id
  // A watcher node runs the telemetry-only agent: the platform refuses every
  // command, so the whole management surface is hidden rather than rendered as
  // buttons that can only fail. (The backend enforces the same; this is UX.)
  const isWatcher = node?.agent_kind === 'watcher'
  const canAnyAction =
    !isWatcher &&
    (check(Actions.NodeReboot, ou) ||
      check(Actions.NodeStatusCheck, ou) ||
      check(Actions.NodeShutdown, ou) ||
      check(Actions.NodeInstallOS, ou) ||
      check(Actions.NodeDelete, ou) ||
      check(Actions.NodeRebootRecovery, ou) ||
      check(Actions.NodeEEPROMFlash, ou) ||
      check(Actions.NodeUpdateAgent, ou) ||
      check(Actions.NodeFileWrite, ou) ||
      check(Actions.NodeReplace, ou) ||
      check(Actions.NodeTerminal, ou) ||
      check(Actions.PlaybookRun, ou) ||
      check(Actions.NodeCredentials, ou))
  // Unit moves, node policy and secret attachments are checked account-wide
  // by their routes, so an OU-scoped grant is not offered them.
  const canPolicy = check(Actions.NodeAssignOU) || check(Actions.SecretManage)
  const canAdvanced =
    !isWatcher &&
    (check(Actions.NodeEEPROMFlash, ou) ||
      check(Actions.NodeFileWrite, ou) ||
      check(Actions.NodeUpdate, ou) ||
      // Re-adopt (adoption file / SD image) lives on the Advanced tab, so a user
      // who only holds the credentials permission must still be able to open it.
      check(Actions.NodeCredentials, ou))
  const tabAvailable = (tab: (typeof NODE_TABS)[number]): boolean => {
    if (tab === 'actions') return canAnyAction
    if (tab === 'advanced') return canAdvanced
    // 'properties' is always available: its Attributes section is read-first,
    // while the policy/access/secrets blocks inside gate themselves.
    return true // overview + properties are read-first
  }
  const visibleTabs = NODE_TABS.filter(tabAvailable)

  const requestedTab: NodeTab = NODE_VIEWS.includes(search.view ?? '')
    ? (search.view as NodeTab)
    : 'overview'
  // Clamp to a permitted tab (a hidden/deep-linked tab falls back to overview).
  const activeTab: NodeTab =
    requestedTab === 'history' ||
    visibleTabs.includes(requestedTab as (typeof NODE_TABS)[number])
      ? requestedTab
      : 'overview'

  const setActiveTab = useCallback(
    (tab: NodeTab) => {
      navigate({
        to: '.',
        search: (old) => ({
          ...old,
          sidebar: SidebarEnum.Node,
          nodeId,
          view: tab === 'overview' ? undefined : tab,
        }),
        replace: true,
      })
    },
    [navigate, nodeId],
  )

  // A sub-view to open right after switching to the Actions tab (e.g. the header
  // pencil opens Edit from any tab). Consumed by the tab-change effect below.
  const pendingActionView = useRef<ActionView | null>(null)

  // Leaving the Actions tab (or switching node) closes any open sub-view —
  // unless one was queued to open on arrival (pendingActionView).
  useEffect(() => {
    if (
      pendingActionView.current &&
      activeTab === viewHostTab(pendingActionView.current)
    ) {
      setActionView(pendingActionView.current)
      pendingActionView.current = null
    } else {
      setActionView(null)
    }
    // The nested detail steps (config-file editor, drive details) must not
    // survive leaving the tab or switching node.
    setEditingFile(null)
    setStorageDevicePath(null)
  }, [activeTab, nodeId])

  // Stable close callback for sub-views. Inline `() => setActionView(null)`
  // changed identity every render, which churned effects that depend on it
  // (the Terminal reconnected on each render → spurious "connection failed").
  const closeActionView = useCallback(() => {
    setActionView(null)
    setEditingFile(null)
    setStorageDevicePath(null)
  }, [])

  // Open an Actions sub-view from anywhere: if already on Actions, open it now;
  // otherwise switch to Actions and let the effect open it on arrival.
  const openActionView = useCallback(
    (view: ActionView) => {
      const host = viewHostTab(view)
      if (activeTab === host) {
        setActionView(view)
      } else {
        pendingActionView.current = view
        setActiveTab(host)
      }
    },
    [activeTab, setActiveTab],
  )

  // Trigger the slide-in transition on each sub-view open.
  useEffect(() => {
    if (!actionView) {
      setSubEnter(false)
      return
    }
    setSubEnter(false)
    const raf = requestAnimationFrame(() => setSubEnter(true))
    return () => cancelAnimationFrame(raf)
  }, [actionView])

  // Fetch node commands when sidebar opens (ensures we have latest data from API)
  useEffect(() => {
    if (nodeId) {
      fetchNodeCommandsFx(nodeId)
    }
  }, [nodeId])

  // Find latest active command (registered, sent, or in_progress)
  // Commands are already sorted by created_at DESC from the store, so first match is latest
  const activeCommand = commands.find(
    (cmd) =>
      cmd.status === 'registered' ||
      cmd.status === 'sent' ||
      cmd.status === 'in_progress',
  )

  // Find last command (most recent, regardless of status)
  const lastCommand = commands.length > 0 ? commands[0] : null

  // One-shot reboot-to-recovery in flight: the status badge and the command
  // bar transition from orange to violet as the node approaches coming back
  // netbooted.
  // Any active reboot for this node counts as "in flight" — not just the
  // latest active command — so the RESTARTING presentation survives banner
  // dismissal, panel reopen, and later queued commands.
  const inFlightReboot = commands.find(
    (cmd) =>
      cmd.slug === 'reboot' &&
      (cmd.status === 'registered' ||
        cmd.status === 'sent' ||
        cmd.status === 'in_progress'),
  )
  const rebootInFlight = !!inFlightReboot
  const rebootToRecoveryInFlight =
    !!node?.recovery_revert_pending && rebootInFlight
  // Pinned for a command that never asked for a reboot. The badge says so
  // instead of announcing a restart the device is not performing.
  const heldForWork = isNodeHeldForWork(node?.status, node?.pinned_by_slug)

  // A bootloader write that has landed on the device but not yet taken effect:
  // the EEPROM is flashed at command time and read at BOOT time.
  //
  // The node row DOES say so — nodes.bootloader_flashed_at /
  // bootloader_reboot_pending, stamped at settle — and that field is the
  // authority (see stagedBootloaderAt below). This command-log derivation
  // stays as the FALLBACK: it wins when the log knows a flash newer than the
  // server's stamp (a settle write that failed, a push that never arrived)
  // and covers rows from a backend that predates the field.
  //
  // Deliberately not keyed on "the mismatch is gone": on a board where the flash
  // does not take, the mismatch persists and the operator needs to be told the
  // reboot already happened rather than being offered the same button forever.
  const bootedAtMs = node?.system_info?.hardware?.boot?.booted_at
    ? node.system_info.hardware.boot.booted_at * 1000
    : undefined
  const stagedBootloaderWrite = useMemo(
    () =>
      commands.find(
        (cmd) =>
          cmd.slug === 'update_boot_order' &&
          cmd.status === 'success' &&
          (bootedAtMs === undefined ||
            new Date(cmd.updated_at).getTime() > bootedAtMs),
      ),
    [commands, bootedAtMs],
  )
  // The server now carries the same fact durably on the node row
  // (bootloader_flashed_at / bootloader_reboot_pending, stamped by the flash
  // settle and retired by the next boot), which also covers the
  // window where this panel's command list has not loaded yet. The log
  // derivation above still wins when it knows a flash NEWER than the server's
  // stamp — a settle write that failed, a node push that never arrived — and
  // covers a row that predates the field; a server "not pending" only stands
  // when no fresher flash is in the log.
  const stagedBootloaderAt = (() => {
    if (node?.bootloader_reboot_pending && node.bootloader_flashed_at) {
      return new Date(node.bootloader_flashed_at * 1000).toISOString()
    }
    const logStagedAt = stagedBootloaderWrite?.updated_at
    if (
      logStagedAt &&
      (!node?.bootloader_flashed_at ||
        new Date(logStagedAt).getTime() > node.bootloader_flashed_at * 1000)
    ) {
      return logStagedAt
    }
    return undefined
  })()
  const recoveryTransition = useTimedProgress(
    rebootToRecoveryInFlight ? inFlightReboot?.created_at : undefined,
    RECOVERY_REBOOT_EXPECTED_MS,
  )

  // A finished command is only worth surfacing as a banner while it is recent;
  // stale results just linger (they stay available in the History tab).
  const RECENT_COMMAND_WINDOW_MS = 5 * 60 * 1000
  const lastCommandIsRecent =
    !!lastCommand &&
    Date.now() - new Date(lastCommand.updated_at).getTime() <
      RECENT_COMMAND_WINDOW_MS

  // A command the agent has gone quiet on is effectively stuck. Auto-hide it
  // (it stays in History) so the banner doesn't linger forever claiming work is
  // under way.
  //
  // A queued command (registered/sent) may simply be ignored — an older agent
  // that doesn't understand a newer command drops it silently. An in_progress
  // one gets a far longer leash because a live long-running command reports
  // progress continuously: only silence past a whole install's worth of time
  // means the agent died mid-execution (update_boot_order, for one, reboots the
  // device to apply the EEPROM and never reports again). The backend fails
  // these on the node's next reconnect; this covers the node that has not
  // reconnected yet.
  const QUEUED_STALE_MS = 3 * 60 * 1000
  const IN_PROGRESS_STALE_MS = 15 * 60 * 1000
  const activeCommandStaleAfterMs =
    activeCommand?.status === 'in_progress'
      ? IN_PROGRESS_STALE_MS
      : QUEUED_STALE_MS
  const activeCommandIsStuck =
    !!activeCommand &&
    Date.now() - new Date(activeCommand.updated_at).getTime() >
      activeCommandStaleAfterMs

  // Commands the operator dismissed (by id) or that auto-completed; hides both
  // the active and the last-command banner. Backed by a module-level cache
  // (dismissedCommandIdsCache) so a dismissal survives the sidebar remounting
  // when the operator navigates away and back — local state reset made the
  // banner reappear. A version counter forces the re-render on change.
  const [, bumpDismissed] = useState(0)
  const dismissCommand = useCallback((id: string) => {
    dismissedCommandIdsCache.add(id)
    bumpDismissed((v) => v + 1)
  }, [])
  const isDismissed = (id: string) => dismissedCommandIdsCache.has(id)

  // Auto-hide a finished command banner after 10s (it stays in History). Only
  // terminal states — an in-progress command keeps its banner until it finishes.
  const lastCommandId = lastCommand?.id
  const lastCommandStatus = lastCommand?.status
  useEffect(() => {
    if (!lastCommandId || !lastCommandIsRecent) return
    if (lastCommandStatus !== 'success' && lastCommandStatus !== 'failed') {
      return
    }
    if (dismissedCommandIdsCache.has(lastCommandId)) return
    const t = setTimeout(() => dismissCommand(lastCommandId), 10000)
    return () => clearTimeout(t)
  }, [lastCommandId, lastCommandStatus, lastCommandIsRecent, dismissCommand])

  // Store node name safely (will be empty string if node is null)
  const nodeName = node?.name || node?.hostname || ''

  const handleDownloadAdoptionFile = useCallback(async () => {
    if (!node) return
    try {
      setIsProcessing(true)
      await apiNodes.downloadAdoptionFile(nodeId, nodeName)
    } catch (error) {
      showError(
        t('widgets.sidebars.node.panel.toasts.adoptionFileError'),
        error,
      )
    } finally {
      setIsProcessing(false)
    }
  }, [nodeId, nodeName, node])

  const handleDownloadNodeSDImage = useCallback(async () => {
    if (!node) return
    try {
      setPreparingSD(true)
      await apiNodes.downloadNodeSDImage(nodeId)
    } catch (error) {
      showError(t('widgets.sidebars.node.panel.toasts.sdImageError'), error)
    } finally {
      setPreparingSD(false)
    }
  }, [nodeId, node])

  const handleReboot = useCallback(async () => {
    try {
      setIsProcessing(true)
      await apiNodes.rebootNode(nodeId)
      await nodesModel.fetchNodesFx()
      await fetchNodeCommandsFx(nodeId)
      showSuccess(t('widgets.sidebars.node.panel.toasts.rebootSent'))
    } catch (error) {
      showError(t('widgets.sidebars.node.panel.toasts.rebootError'), error)
    } finally {
      setIsProcessing(false)
    }
  }, [nodeId])

  const handleRebootToRecovery = useCallback(async () => {
    setPendingConfirm(null)
    try {
      setIsProcessing(true)
      await apiNodes.postRebootToRecovery(nodeId)
      await nodesModel.fetchNodesFx()
      await fetchNodeCommandsFx(nodeId)
      showSuccess(t('widgets.sidebars.node.panel.toasts.recoverySent'))
    } catch (error) {
      showError(t('widgets.sidebars.node.panel.toasts.recoveryError'), error)
    } finally {
      setIsProcessing(false)
    }
  }, [nodeId])

  const handleClearBootFailsafe = useCallback(async () => {
    try {
      setIsProcessing(true)
      await apiNodes.postClearBootFailsafe(nodeId)
      await nodesModel.fetchNodesFx()
      await fetchNodeCommandsFx(nodeId)
      showSuccess(t('widgets.sidebars.node.panel.toasts.failsafeCleared'))
    } catch (error) {
      showError(t('widgets.sidebars.node.panel.toasts.failsafeError'), error)
    } finally {
      setIsProcessing(false)
    }
  }, [nodeId])

  const handleShutdown = useCallback(async () => {
    try {
      setIsProcessing(true)
      await apiNodes.shutdownNode(nodeId)
      await nodesModel.fetchNodesFx()
      await fetchNodeCommandsFx(nodeId)
      showSuccess(t('widgets.sidebars.node.panel.toasts.shutdownSent'))
    } catch (error) {
      showError(t('widgets.sidebars.node.panel.toasts.shutdownError'), error)
    } finally {
      setIsProcessing(false)
    }
  }, [nodeId])

  const handleDelete = useCallback(async () => {
    setPendingConfirm(null)
    try {
      setIsProcessing(true)
      // force mirrors what the dialog just said. The platform refuses a
      // connected delete without it, and the confirmation IS the operator's
      // explicit acknowledgement — see DELETE_ONLINE_NODE_WARNING. The
      // predicate must stay the backend's IsInConnectedState, not "online":
      // encoding the narrower rule here made a node wedged mid-install
      // undeletable from this panel.
      await apiNodes.deleteNode(nodeId, isNodeInConnectedState(node?.status))
      await nodesModel.fetchNodesFx()
      // Closed only once the server has agreed. It used to close FIRST, so a
      // refused delete (403, or a node the backend would not remove) looked
      // identical to a successful one: the panel went away, the error went to
      // the console, and the node quietly reappeared in the list.
      showSuccess(
        t('widgets.sidebars.node.panel.toasts.deleted', {
          name:
            nodeName ||
            t('widgets.sidebars.node.panel.toasts.deletedFallbackName'),
        }),
      )
      onClose()
    } catch (error) {
      showError(t('widgets.sidebars.node.panel.toasts.deleteError'), error)
    } finally {
      setIsProcessing(false)
    }
  }, [nodeId, nodeName, node?.status, onClose])

  const handleApprove = useCallback(async () => {
    try {
      setIsProcessing(true)
      await apiNodes.approveNode(nodeId)
      await nodesModel.fetchNodesFx()
      await fetchNodeCommandsFx(nodeId)
      showSuccess(t('widgets.sidebars.node.panel.toasts.approved'))
    } catch (error) {
      showError(
        t('widgets.sidebars.node.panel.toasts.approveError'),
        deviceCapReasonText(error) ?? error,
      )
    } finally {
      setIsProcessing(false)
    }
  }, [nodeId])

  const handleReject = useCallback(async () => {
    setPendingConfirm(null)
    try {
      setIsProcessing(true)
      await apiNodes.rejectNode(nodeId)
      await nodesModel.fetchNodesFx()
      await fetchNodeCommandsFx(nodeId)
      showSuccess(t('widgets.sidebars.node.panel.toasts.rejected'))
    } catch (error) {
      showError(t('widgets.sidebars.node.panel.toasts.rejectError'), error)
    } finally {
      setIsProcessing(false)
    }
  }, [nodeId])

  // Early return if node doesn't exist - AFTER all hooks are called
  if (!node) {
    return null
  }

  // Connection identity rows. Bootloader is rendered separately (it carries a
  // freshness badge), so it is not part of this list.
  const na = t('widgets.sidebars.node.panel.na')
  const connectionParams = [
    {
      param: t('widgets.sidebars.node.params.ip'),
      value: node.ipv4 || na,
    },
    {
      param: t('widgets.sidebars.node.params.mac'),
      value: node.mac_address || node.fingerprint?.mac_address || na,
    },
    {
      param: t('widgets.sidebars.node.params.hwSerial'),
      value: node.fingerprint?.hardware_serial || na,
    },
  ]

  // Bootloader row value (short hash + build date), when the device has
  // reported its EEPROM firmware. Shortened so the 40-char git hash the agent
  // reports does not overflow the row.
  // Whether the bootloader row is a LIVE reading or a stored one. Only a
  // connected full agent reports it; see the row for why.
  //
  // CONNECTED, not "online": maintenance and restarting are connected states
  // with a live stream — a node mid-command is reporting as freshly as any
  // other — and calling that a memory contradicted the tooltip beside it,
  // which says "the node is not connected". It also pushed a suffix onto a row
  // that had no space for it, so the marker rendered as "(…" on exactly the
  // nodes it was wrong about.
  const bootloaderIsLive = !isWatcher && isNodeInConnectedState(node.status)
  const bootloaderStaleReason = isWatcher
    ? t('widgets.sidebars.node.panel.bootloaderRow.staleWatcher')
    : t('widgets.sidebars.node.panel.bootloaderRow.staleOffline')

  const bootloaderValue = node.bootloader_version
    ? node.bootloader_timestamp
      ? `${shortBootloaderVersion(node.bootloader_version)} (${new Date(node.bootloader_timestamp * 1000).toISOString().slice(0, 10)})`
      : shortBootloaderVersion(node.bootloader_version)
    : null

  // Get hardware info from system_info (nested structure)
  const hardware = node?.system_info?.hardware
  const cpu = hardware?.cpu
  const ram = hardware?.ram
  const tpm = hardware?.tpm

  const cpuVendor = cpu?.vendor
  const cpuModel = cpu?.model
  const boardRevision = cpu?.board_revision
  const cores = cpu?.cores
  const ramGB = ram?.size ? (ram.size / (1024 * 1024 * 1024)).toFixed(0) : null

  // Device model (CM4/CM5/Pi …) shown as its own row above CPU; full model
  // string kept for the tooltip.
  const deviceModel = deviceModelLabel(cpuModel, boardRevision)

  // CPU descriptor: ARM core name + core count + max clock. The agent does not
  // report clock, so the max is config.txt arm_freq when overclocked, else the
  // model's stock rating. Falls back to vendor/model on non-Pi hardware.
  // Is the node running the netboot bootstrap right now? The agent states this
  // outright (recovery_bootstrap, since 0.4.5); the boot-device and Buildroot
  // checks are fallbacks for older reports. Anything derived from the INSTALLED
  // system's stored files has to be qualified by this.
  const inRecovery =
    node?.system_info?.hardware?.boot?.recovery_bootstrap === true ||
    node?.system_info?.hardware?.boot?.current_boot_device === 'http' ||
    (node?.system_info?.os?.details || '').toLowerCase().includes('buildroot')

  const cpuCore = cpuCoreName(cpu?.info)
  //
  // In recovery the arm_freq is NOT applied: the node is running the netboot
  // bootstrap from RAM, whose own config.txt sets no clock directives at all,
  // so the installed system's overclock says nothing about the CPU in front of
  // you. Showing the installed overclock here would claim a clock the CPU is
  // not running.
  const cpuMaxFreqMHz =
    (inRecovery ? null : overclock?.armFreqMHz) ??
    stockMaxFreqMHz(cpuModel, boardRevision)
  const cpuFreqText = formatFreq(cpuMaxFreqMHz)
  const cpuBase = cpuCore || cpuVendor || cpuModel || null
  const cpuTagText =
    [
      cpuBase,
      cores
        ? t('widgets.sidebars.node.panel.infoRows.coresCount', { cores })
        : null,
      cpuFreqText,
    ]
      .filter(Boolean)
      .join(' · ') || null

  // Get OS info
  const osInfo = node?.system_info?.os
  const osTagText = osInfo?.details || osInfo?.version || null
  // The netboot bootstrap runs Buildroot; on it EEPROM updates are always
  // disabled and cannot be enabled from the UI, so the "EEPROM is disabled"
  // banner is pure noise there. Only surface it on a managed (installed) OS.
  const isBootstrap = osTagText?.toLowerCase().includes('buildroot') || false

  // Provisioning actions (Install OS / Install Agent / Provision Connection)
  // only make sense while the device is netbooted into the maintenance
  // bootstrap — on a managed OS booted from local storage they would target the
  // running disk and are refused by the agent. Detect the netboot state from the
  // Buildroot OS marker or an http current-boot device.
  const currentBootDevice =
    node?.system_info?.hardware?.boot?.current_boot_device
  const inMaintenance = isBootstrap || currentBootDevice === 'http'

  // Every reported medium on one Storage row, largest first, with the device the
  // node is running from marked. Needs currentBootDevice, which is why it sits
  // below it.
  const storageLines = buildStorageLines(
    node?.system_info?.storage_devices || node?.storage_devices,
    currentBootDevice,
    node?.system_info?.hardware?.boot?.root_device,
  )

  // Storage devices the node reported (drives the Advanced → Storage row, its
  // list and the per-drive detail step). A device that disappears between
  // reports simply resolves to undefined, which falls back to the list.
  const storageDevices =
    node?.system_info?.storage_devices || node?.storage_devices || {}
  const hasStorageDevices = Object.keys(storageDevices).length > 0
  const selectedStorageDevice = storageDevicePath
    ? storageDevices[storageDevicePath]
    : undefined

  // TPM row text — only when a TPM is actually present.
  const tpmLabel = t('widgets.sidebars.node.panel.infoRows.tpm')
  let tpmText: string | null = null
  if (tpm?.present) {
    tpmText = tpmLabel
    if (tpm.version) tpmText = `${tpmLabel} ${tpm.version}`
    if (tpm.model) {
      tpmText = tpm.version
        ? `${tpmText} (${tpm.model})`
        : `${tpmLabel} ${tpm.model}`
    }
  }

  // Attached non-storage USB devices. Empty on agents below 0.5.5, which is why
  // the row is omitted rather than showing "none": absent means nobody looked.
  const usbDevices = node?.system_info?.hardware?.usb ?? []

  // Power / thermal throttling. Null when the node reported nothing, so the row
  // is omitted rather than showing an "OK" nothing measured.
  const powerLine = powerSummary(node?.system_info?.hardware?.power)

  // Whether the Configuration section has anything to show.
  const hasConfiguration =
    !!deviceModel ||
    !!cpuTagText ||
    !!ramGB ||
    !!osTagText ||
    storageLines.length > 0 ||
    usbDevices.length > 0 ||
    !!powerLine ||
    !!tpmText

  const isReadyForAdoption = node.status === NodeStatusReadyForAdoption

  // The command shown as a header chip: the active one, else the recent finished
  // one — whichever is not dismissed. Same selection the content banners used.
  const headerCommand =
    activeCommand && !activeCommandIsStuck && !isDismissed(activeCommand.id)
      ? activeCommand
      : (!activeCommand || activeCommandIsStuck) &&
          lastCommand &&
          lastCommandIsRecent &&
          !isDismissed(lastCommand.id)
        ? lastCommand
        : null

  const showTabs = !isReadyForAdoption && node.approval_status === 'approved'
  const isRaspberryPi =
    !!cpuModel && cpuModel.toLowerCase().includes('raspberry pi')

  return (
    <div>
      {/* Header: close control + status badge + uptime, then the node's
          identity. Sticky, because the control that CLOSES the panel scrolling
          out of reach is the one thing a header must never do — the scroll
          container is the panel wrapper in _mainLayout, so top-0 pins to it.
          The identity row is part of it: scrolled a long node's content and you
          lose track of which node you are looking at, and half a heading
          sliding under the status row reads as a rendering fault. Opaque
          background and a z-index so content passes underneath rather than
          through it. */}
      <div className="sticky top-0 z-20 bg-neutral-14 flex flex-col gap-1 px-5 pt-4 pb-3 border-b border-neutral-12">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <button
              type="button"
              onClick={onClose}
              aria-label={t('widgets.sidebars.closePanel')}
              title={t('widgets.sidebars.closePanel')}
              className="flex items-center justify-center w-6 h-6 rounded border border-neutral-11 text-neutral-6 hover:text-neutral-2 hover:border-neutral-9 transition-colors"
            >
              <svg
                className="w-3.5 h-3.5"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M9 5l7 7-7 7"
                />
              </svg>
            </button>
            {/* While a reboot is in flight the dropped connection is expected —
                present RESTARTING instead of flashing OFFLINE. */}
            <NodeStatusBadge
              status={
                rebootInFlight && node.status === 'offline'
                  ? 'restarting'
                  : node.status
              }
              label={
                isNetbootRecovery(node)
                  ? t('widgets.sidebars.node.panel.badge.recovery')
                  : heldForWork
                    ? t('entities.nodeList.nodeCard.status.working')
                    : !rebootInFlight &&
                        node.status === 'offline' &&
                        node.awaiting_return_after
                      ? t('widgets.sidebars.node.panel.badge.neverReturned')
                      : undefined
              }
              color={
                isNetbootRecovery(node)
                  ? 'violet'
                  : heldForWork
                    ? 'blue'
                    : !rebootInFlight &&
                        node.status === 'offline' &&
                        node.awaiting_return_after
                      ? 'amber'
                      : undefined
              }
              style={
                rebootToRecoveryInFlight && !isNetbootRecovery(node)
                  ? rebootToRecoveryBadgeStyle(recoveryTransition)
                  : undefined
              }
            />
          </div>
          <div className="flex items-center gap-3">
            {/* Identify: blink the physical node's locate LED. Works on any
                Pi (ACT LED); boards with digital LEDs blink prettier. Online
                only — a locate that fires after the operator left the rack is
                a surprise, so offline it is not offered at all. */}
            {showTabs &&
              node.status === 'online' &&
              node.agent_kind !== 'watcher' &&
              check(Actions.NodeStatusCheck, node.ou_id) && (
                <button
                  type="button"
                  disabled={identifyBusy}
                  onClick={async () => {
                    setIdentifyBusy(true)
                    try {
                      const { duration_sec } = await identifyNode(nodeId)
                      // Light the list's ID lamp too — the blink is a fact
                      // about the node, not about which control started it.
                      // The server answers with the account's effective
                      // duration; the lamp runs exactly as long as the device.
                      identifyBlinkStarted({
                        nodeId,
                        durationMs: duration_sec * 1000,
                      })
                      showSuccess(
                        t(
                          'widgets.sidebars.node.panel.toasts.identifyBlinking',
                          {
                            duration: formatBlink(duration_sec),
                          },
                        ),
                      )
                    } catch (e) {
                      showError(
                        e instanceof Error && 'response' in e
                          ? ((
                              e as {
                                response?: { data?: { message?: string } }
                              }
                            ).response?.data?.message ??
                              t(
                                'widgets.sidebars.node.panel.toasts.identifyFailed',
                              ))
                          : t(
                              'widgets.sidebars.node.panel.toasts.identifyFailed',
                            ),
                      )
                    } finally {
                      setIdentifyBusy(false)
                    }
                  }}
                  aria-label={t(
                    'widgets.sidebars.node.panel.header.identifyAria',
                  )}
                  title={t('widgets.sidebars.node.panel.header.identifyTitle')}
                  className={clsx(
                    'flex items-center justify-center transition-colors',
                    identifyBusy
                      ? 'text-lime-6 animate-pulse'
                      : 'text-neutral-6 hover:text-neutral-2',
                  )}
                >
                  <svg
                    className="w-4 h-4"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth={2}
                    viewBox="0 0 24 24"
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="M9.663 17h4.674M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0013.5 19.5h-3a3.374 3.374 0 00-1.488-2.803l-.548-.547z"
                    />
                  </svg>
                </button>
              )}
            {showTabs && (
              <button
                type="button"
                onClick={() => openActionView('history')}
                aria-label={t(
                  'widgets.sidebars.node.panel.header.commandHistory',
                )}
                title={t('widgets.sidebars.node.panel.header.commandHistory')}
                className={clsx(
                  'flex items-center justify-center transition-colors',
                  actionView === 'history'
                    ? 'text-lime-6'
                    : 'text-neutral-6 hover:text-neutral-2',
                )}
              >
                <svg
                  className="w-4 h-4"
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                >
                  <circle cx="12" cy="12" r="9" strokeWidth={2} />
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M12 7v5l3 2"
                  />
                </svg>
              </button>
            )}
            {/* Edit node info \u2014 pencil, borderless to match the history clock
                icon; grouped with it on the left of the cluster. */}
            {node.approval_status === 'approved' &&
              check(Actions.NodeUpdate, node.ou_id) && (
                <button
                  type="button"
                  onClick={() => setActiveTab('properties')}
                  aria-label={t(
                    'widgets.sidebars.node.panel.header.nodeProperties',
                  )}
                  title={t('widgets.sidebars.node.panel.header.nodeProperties')}
                  className={clsx(
                    'flex items-center justify-center transition-colors',
                    activeTab === 'properties'
                      ? 'text-lime-6'
                      : 'text-neutral-6 hover:text-neutral-2',
                  )}
                >
                  <svg
                    className="w-4 h-4"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth={2}
                    viewBox="0 0 24 24"
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z"
                    />
                  </svg>
                </button>
              )}
            {/* The label comes from the same module as the value: a node that
                is down returns a last-seen time, and a hardcoded "uptime:"
                turned that into "uptime: 5m ago". The mid-reboot dash is no
                longer corrected here either \u2014 useUptime answers every
                transitional state with one now. */}
            <div className="text-sm text-neutral-7 whitespace-nowrap">
              {uptimeLabel(node?.status || 'offline')}: {uptime}
            </div>
          </div>
        </div>

        {/* Identity (always) */}
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={() =>
              nodesModel.toggleFavoriteFx({ nodeId, favorite: !isFavorite })
            }
            aria-label={
              isFavorite
                ? t('widgets.sidebars.node.panel.header.removeFavorite')
                : t('widgets.sidebars.node.panel.header.addFavorite')
            }
            title={
              isFavorite
                ? t('widgets.sidebars.node.panel.header.removeFavorite')
                : t('widgets.sidebars.node.panel.header.addFavorite')
            }
            className={clsx(
              'flex-shrink-0 transition-colors',
              isFavorite
                ? 'text-lime-6'
                : 'text-neutral-7 hover:text-neutral-4',
            )}
          >
            <svg
              className="w-4 h-4"
              viewBox="0 0 20 20"
              fill={isFavorite ? 'currentColor' : 'none'}
              stroke="currentColor"
              strokeWidth={isFavorite ? 0 : 1.5}
            >
              <path d="M9.049 2.927c.3-.921 1.603-.921 1.902 0l1.07 3.292a1 1 0 00.95.69h3.462c.969 0 1.371 1.24.588 1.81l-2.8 2.034a1 1 0 00-.364 1.118l1.07 3.292c.3.921-.755 1.688-1.54 1.118l-2.8-2.034a1 1 0 00-1.175 0l-2.8 2.034c-.784.57-1.838-.197-1.539-1.118l1.07-3.292a1 1 0 00-.364-1.118L2.98 8.72c-.783-.57-.38-1.81.588-1.81h3.461a1 1 0 00.951-.69l1.07-3.292z" />
            </svg>
          </button>
          <div className="flex-1 min-w-0">
            <h2 className="font-bold uppercase truncate">
              {nodeDisplayName(node, preferHostnames) || '—'}
            </h2>
            {node.hostname && node.name && node.hostname !== node.name && (
              <div className="text-sm text-neutral-7 font-normal truncate">
                {preferHostnames ? node.name : node.hostname}
              </div>
            )}
          </div>
          {/* Command status as a compact chip here (not a full-width bar below),
              so a command appearing/finishing never shifts the tab content. */}
          {node.approval_status === 'approved' && headerCommand && (
            <CommandChip
              command={headerCommand}
              onClick={() => openActionView('history')}
              onDismiss={() => dismissCommand(headerCommand.id)}
            />
          )}
        </div>
      </div>

      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        {/* Failed netboot attempts, BEFORE the failsafe has anything to say.
            The count is maintained server-side and was rendered nowhere, so a
            node re-netbooting over and over was invisible until the failsafe
            fired — and when no threshold is armed it never fires, so this is
            the only signal that exists. With no threshold armed,
            nothing is going to intervene, and the panel says so rather than
            implying a rescue is coming. */}
        {node.boot_failsafe_at == null &&
          (node.boot_attempt_count ?? 0) > 0 && (
            <div className="rounded border border-amber-6/50 bg-amber-6/10 px-3 py-2 text-xs">
              <div className="font-medium text-amber-6">
                {t('widgets.sidebars.node.panel.banners.bootAttempts', {
                  count: node.boot_attempt_count ?? 0,
                })}
              </div>
              <div className="mt-1 text-neutral-4">
                {/* boot_failsafe_threshold is the per-NODE override and is absent
                  for most nodes; the install-wide value is not exposed to the
                  client, so this must not claim a rescue is coming. It says
                  what is measured and what to do about it. */}
                {t('widgets.sidebars.node.panel.banners.checkInstalledOS')}
                {(node.boot_failsafe_threshold ?? 0) > 0
                  ? t('widgets.sidebars.node.panel.banners.failsafeServes', {
                      attempts: node.boot_failsafe_threshold,
                    })
                  : ''}
              </div>
            </div>
          )}

        {/* Boot-loop failsafe: the platform served recovery after repeated
            withheld netboots (a crash loop). Surface it and let an operator
            acknowledge once they have investigated. */}
        {node.boot_failsafe_at != null && (
          <div className="rounded border border-amber-6/50 bg-amber-6/10 px-3 py-2 text-xs">
            <div className="font-medium text-amber-6">
              {t('widgets.sidebars.node.panel.banners.bootLoopTitle')}
            </div>
            <div className="mt-1 text-neutral-4">
              {t('widgets.sidebars.node.panel.banners.bootLoopBody')}
            </div>
            {check(Actions.NodeUpdate, node.ou_id) && (
              <Button
                variant="normal"
                kind="secondary"
                onClick={handleClearBootFailsafe}
                disabled={isProcessing}
                className="mt-2 text-xs"
              >
                {t('widgets.sidebars.node.panel.banners.acknowledge')}
              </Button>
            )}
          </div>
        )}

        {/* Bootloader not set up for network boot on this install (e.g. after
            moving the node to another account/install). Guides the one-click
            update -> reboot-to-apply flow. */}
        {/* NOT on a Watcher node. The "no boot host reported" arm below reads
            ABSENCE of a report as the negative fact "this device is not set up
            for network boot" — and the telemetry-only agent cannot report it at
            all: boot_http_host comes from rpi-eeprom-config and the version from
            vcgencmd, both of which need root the watcher deliberately does not
            have. So the banner fired on every Watcher node, permanently, while
            the same device's stored boot order said network-first; and its
            button dispatched provision-eeprom, which the platform refuses for an
            watcher. An action that cannot succeed, offered over a claim the
            platform cannot make. */}
        {node.approval_status === 'approved' &&
          !isBootstrap &&
          !isWatcher &&
          isRaspberryPi &&
          caps.netboot &&
          (bootHostMismatch || (!!caps.boot_host && !bootHost)) && (
            <BootloaderMismatchBanner
              nodeId={nodeId}
              online={node.status === 'online'}
              canProvision={caps.eeprom_signer}
              pointsElsewhere={bootHostMismatch}
              stagedAt={stagedBootloaderAt}
              rebootStartedAt={inFlightReboot?.created_at}
            />
          )}

        {/* Pending approval banner */}
        {node.approval_status === 'pending_approval' && (
          <div className="flex flex-col gap-3 p-4 bg-orange-6/10 border border-orange-6/30 rounded-lg">
            <div className="flex items-center gap-2">
              <svg
                className="w-5 h-5 text-orange-6"
                fill="currentColor"
                viewBox="0 0 20 20"
              >
                <path
                  fillRule="evenodd"
                  d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z"
                  clipRule="evenodd"
                />
              </svg>
              <span className="font-medium text-orange-6">
                {t('widgets.sidebars.node.panel.banners.pendingTitle')}
              </span>
            </div>
            <p className="text-sm text-neutral-4">
              {t('widgets.sidebars.node.panel.banners.pendingBody')}
            </p>
            {check(Actions.NodeApprove, node.ou_id) && (
              <div className="flex gap-2">
                <Button
                  size="md"
                  onClick={handleApprove}
                  disabled={isProcessing}
                  grow
                >
                  {t('widgets.sidebars.node.panel.banners.approve')}
                </Button>
                <Button
                  variant="normal"
                  kind="secondary"
                  size="md"
                  onClick={() => setPendingConfirm('reject')}
                  disabled={isProcessing}
                >
                  {t('widgets.sidebars.node.panel.banners.reject')}
                </Button>
              </div>
            )}
          </div>
        )}

        {/* Rejected banner */}
        {node.approval_status === 'rejected' && (
          <div className="flex flex-col gap-3 p-4 bg-red-6/10 border border-red-6/30 rounded-lg">
            <div className="flex items-center gap-2">
              <svg
                className="w-5 h-5 text-red-6"
                fill="currentColor"
                viewBox="0 0 20 20"
              >
                <path
                  fillRule="evenodd"
                  d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z"
                  clipRule="evenodd"
                />
              </svg>
              <span className="font-medium text-red-6">
                {t('widgets.sidebars.node.panel.banners.rejectedTitle')}
              </span>
            </div>
            <p className="text-sm text-neutral-4">
              {t('widgets.sidebars.node.panel.banners.rejectedBody')}
            </p>
            {check(Actions.NodeApprove, node.ou_id) && (
              <div className="flex gap-2">
                <Button
                  size="md"
                  onClick={handleApprove}
                  disabled={isProcessing}
                  grow
                >
                  {t('widgets.sidebars.node.panel.banners.approve')}
                </Button>
              </div>
            )}
          </div>
        )}

        {/* Command status lives as a compact chip in the header identity row
            (see CommandChip) so it never shifts the tab content. */}

        {/* WebUSB-flashed node: EEPROM already flashed, no media step — it just
            needs to reboot, netboot and connect for the first time. */}
        {isReadyForAdoption && node.awaiting_first_connect && (
          <div className="bg-violet-6/10 border border-violet-6/40 text-violet-3 px-4 py-3 rounded text-sm">
            {t('widgets.sidebars.node.panel.adoption.usbFlashedIntro')}{' '}
            <span className="text-neutral-2">
              {t('widgets.sidebars.node.panel.adoption.usbFlashedHighlight')}
            </span>
            {t('widgets.sidebars.node.panel.adoption.usbFlashedOutro')}
          </div>
        )}

        {/* Adoption (ready_for_adoption, non-USB) — credential-bearing reads.
            The one-command install works on any deployment (device already
            runs an OS, agent dials out); the SD bundle additionally needs the
            server's provisioning payload (caps.sd_provisioning). */}
        {isReadyForAdoption &&
          !node.awaiting_first_connect &&
          check(Actions.NodeCredentials, ou) && (
            <div className="flex flex-col gap-3">
              {caps.one_command_adopt && (
                <InstallCommandBlock nodeId={nodeId} />
              )}
              {caps.sd_provisioning && (
                <>
                  <div className="text-sm text-neutral-4">
                    {t('widgets.sidebars.node.panel.adoption.sdIntro')}{' '}
                    <span className="text-neutral-2">
                      {t(
                        'widgets.sidebars.node.panel.adoption.sdImageHighlight',
                      )}
                    </span>
                    {t('widgets.sidebars.node.panel.adoption.sdOutro')}
                  </div>
                  <SourceOfferNote subject="bundle" />
                  <div className="flex flex-col gap-2">
                    <Button
                      size="lg"
                      onClick={handleDownloadNodeSDImage}
                      disabled={preparingSD}
                      grow
                    >
                      {preparingSD
                        ? t(
                            'widgets.sidebars.node.panel.adoption.preparingImage',
                          )
                        : t(
                            'widgets.sidebars.node.panel.adoption.downloadSdImage',
                          )}
                    </Button>
                    <Button
                      size="lg"
                      kind="secondary"
                      onClick={handleDownloadAdoptionFile}
                      disabled={preparingSD}
                      grow
                    >
                      {t(
                        'widgets.sidebars.node.panel.adoption.downloadAdoptionFile',
                      )}
                    </Button>
                  </div>
                  {preparingSD && (
                    <div className="text-xs text-neutral-5">
                      {t('widgets.sidebars.node.panel.adoption.buildingBundle')}
                    </div>
                  )}
                </>
              )}
              {!caps.sd_provisioning && (
                <Button
                  size="md"
                  kind="secondary"
                  onClick={handleDownloadAdoptionFile}
                  grow
                >
                  {t(
                    'widgets.sidebars.node.panel.adoption.downloadAdoptionFile',
                  )}
                </Button>
              )}
            </div>
          )}

        {/* Delete — a ready_for_adoption node has no hardware bound yet, so it is
            safe to remove (e.g. a mis-created or failed SSH-adopt placeholder).
            The confirm dialog + handler live in the main render below. */}
        {isReadyForAdoption && check(Actions.NodeDelete, ou) && (
          <Button
            size="md"
            kind="secondary"
            variant="destructive"
            onClick={() => setPendingConfirm('delete')}
            grow
          >
            {t('widgets.sidebars.node.panel.actions.deleteNode')}
          </Button>
        )}

        {/* Connection details inline for non-tabbed (pending/rejected) nodes */}
        {!isReadyForAdoption && !showTabs && (
          <div className="grid grid-cols-[min-content,auto] gap-x-5.5 gap-y-2 py-2 items-center text-sm">
            {connectionParams.map(({ param, value }) => (
              <Fragment key={param}>
                <div className="min-w-20 text-neutral-7 whitespace-nowrap">
                  {param}:
                </div>
                <div className="text-lime-6 whitespace-nowrap truncate">
                  {value}
                </div>
              </Fragment>
            ))}
          </div>
        )}

        {/* Tab bar — only for approved, fully-detailed nodes */}
        {showTabs && (
          <div className="flex gap-1 border-b border-neutral-12 -mx-1">
            {visibleTabs.map((tab) => (
              <button
                key={tab}
                type="button"
                onClick={() => setActiveTab(tab)}
                className={clsx(
                  'px-3 py-2 text-xs uppercase tracking-wider border-b-2 -mb-px transition-colors',
                  activeTab === tab
                    ? 'text-lime-6 border-lime-6'
                    : 'text-neutral-6 border-transparent hover:text-neutral-3',
                )}
              >
                {tabLabels[tab]}
              </button>
            ))}
          </div>
        )}

        {/* ===== Tab content (approved nodes) ===== */}
        {showTabs && activeTab === 'overview' && (
          <>
            {/* Connection + identity + Configuration all read as one aligned
                two-column table: a fixed label column and a value column. The
                Configuration block is separated only by a labelled divider so
                it visually continues the same table rather than starting a new
                card. */}
            <div className="flex flex-col gap-2 py-2">
              <InfoRow
                label={t('widgets.sidebars.node.panel.infoRows.hostname')}
              >
                <div className="flex items-center justify-between gap-2 h-7">
                  <span className="block truncate font-mono">
                    {node.hostname || '—'}
                  </span>
                  {node.hostname && (
                    <CopyButton
                      value={node.hostname}
                      label={t('widgets.sidebars.node.panel.infoRows.hostname')}
                    />
                  )}
                </div>
              </InfoRow>

              <NodeUnitRow nodeId={nodeId} />

              {connectionParams.map(({ param, value }) => (
                <InfoRow key={param} label={param}>
                  <div className="flex items-center justify-between gap-2 h-7">
                    <span className="block truncate">{value}</span>
                    {value && value !== na && (
                      <CopyButton value={value} label={param} />
                    )}
                  </div>
                </InfoRow>
              ))}

              {bootloaderValue && (
                <InfoRow
                  label={t('widgets.sidebars.node.panel.bootloaderRow.label')}
                >
                  <div className="flex items-center justify-between gap-2 h-7">
                    {/* LAST KNOWN, not live, in two cases — and it used to say
                        so in only one.
                        
                        A Watcher node never reports bootloader state at all (the
                        telemetry-only agent runs unprivileged and vcgencmd needs
                        root). An OFFLINE full-agent node reported it once, on a
                        connect that has since ended: `nodes.bootloader_version`
                        has exactly one writer, the agent over authenticated
                        gRPC, so with no agent connected the row is a memory. The
                        suffix was gated on isWatcher alone, which presented a
                        stored value as a live one on every disconnected node.
                        
                        Said in the tooltip rather than as another row: the value
                        is still the best answer there is, it just is not
                        current. */}
                    <span
                      className="block truncate"
                      title={
                        bootloaderIsLive ? undefined : bootloaderStaleReason
                      }
                    >
                      {bootloaderValue}
                    </span>
                    {/* Outside the truncating span and unshrinkable: inside
                        it, this competed with the hash and the badge for one
                        row and lost, which turned a qualifier into "(…" — a
                        marker that reads as a broken value rather than a
                        stale one. The full sentence stays in the title above. */}
                    {!bootloaderIsLive && (
                      <span
                        className="flex-shrink-0 text-xxs uppercase tracking-wide text-neutral-7"
                        title={bootloaderStaleReason}
                      >
                        {t(
                          'widgets.sidebars.node.panel.bootloaderRow.lastKnown',
                        )}
                      </span>
                    )}
                    {bootloaderFreshness === 'current' && (
                      <span className="flex-shrink-0 text-xxs uppercase tracking-wide text-green-6">
                        {t(
                          'widgets.sidebars.node.panel.bootloaderRow.upToDate',
                        )}
                      </span>
                    )}
                    {bootloaderFreshness === 'outdated' &&
                      (canAdvanced ? (
                        // Overview only reports; the update lives in Advanced →
                        // Bootloader / EEPROM. Link straight there rather than
                        // making the operator hunt for where it is installed.
                        <button
                          type="button"
                          onClick={() => openActionView('bootloader')}
                          title={t(
                            'widgets.sidebars.node.panel.bootloaderRow.openBootloaderTitle',
                          )}
                          className="flex-shrink-0 text-xxs uppercase tracking-wide text-yellow-6 hover:text-yellow-5 underline decoration-dotted underline-offset-2 transition-colors"
                        >
                          {t(
                            'widgets.sidebars.node.panel.bootloaderRow.updateAvailable',
                          )}
                        </button>
                      ) : (
                        <span className="flex-shrink-0 text-xxs uppercase tracking-wide text-yellow-6">
                          {t(
                            'widgets.sidebars.node.panel.bootloaderRow.updateAvailable',
                          )}
                        </span>
                      ))}
                  </div>
                </InfoRow>
              )}

              <AgentVersionBadge
                nodeId={nodeId}
                version={node.agent_version}
                kind={node.agent_kind}
                online={node.status === 'online'}
                canUpdate={!isWatcher && check(Actions.NodeUpdateAgent, ou)}
              />
            </div>

            {hasConfiguration && (
              <div className="flex flex-col gap-2">
                {/* Labelled divider — continues the table above. */}
                <SectionDivider
                  label={t(
                    'widgets.sidebars.node.panel.infoRows.configuration',
                  )}
                />

                {deviceModel && (
                  <InfoRow
                    label={t('widgets.sidebars.node.panel.infoRows.model')}
                  >
                    <span className="block truncate" title={cpuModel}>
                      {deviceModel}
                    </span>
                  </InfoRow>
                )}

                {/* Resolved platform (carrier board declaration). The row
                    self-hides for nodes that neither have one nor could take
                    one, so a plain Pi panel is unchanged. Read-only here:
                    Overview shows facts, Properties changes them. */}
                <PlatformRow
                  nodeId={nodeId}
                  onOpenProperties={() => setActiveTab('properties')}
                />

                {cpuTagText && (
                  <InfoRow
                    label={t('widgets.sidebars.node.panel.infoRows.cpu')}
                  >
                    <div className="flex items-center gap-2 flex-wrap">
                      <span>{cpuTagText}</span>
                      {overclock?.active && (
                        <span
                          // The badge describes the INSTALLED system's
                          // config.txt, read from a stored copy — never a live
                          // probe. Say which of the two it is, and how old the
                          // reading is, so a week-old file cannot pass for the
                          // state of the machine in front of you.
                          title={[
                            inRecovery
                              ? t(
                                  'widgets.sidebars.node.panel.overview.ocInstalledTitle',
                                )
                              : t(
                                  'widgets.sidebars.node.panel.overview.ocActiveTitle',
                                ),
                            overclock.settings.length > 0
                              ? overclock.settings.join(', ')
                              : null,
                            overclock.readAt
                              ? t(
                                  'widgets.sidebars.node.panel.overview.ocReadFrom',
                                  {
                                    ago: formatAgo(overclock.readAt),
                                  },
                                )
                              : t(
                                  'widgets.sidebars.node.panel.overview.ocReadingAgeUnknown',
                                ),
                          ]
                            .filter(Boolean)
                            .join(' — ')}
                          className={clsx(
                            'flex-shrink-0 text-xxs font-medium uppercase tracking-wide px-1.5 py-0.5 rounded border',
                            inRecovery
                              ? 'border-neutral-7/40 bg-neutral-7/10 text-neutral-5'
                              : 'border-amber-6/40 bg-amber-6/10 text-amber-6',
                          )}
                        >
                          {inRecovery
                            ? t(
                                'widgets.sidebars.node.panel.overview.ocInstalledBadge',
                              )
                            : t('widgets.sidebars.node.panel.overview.ocBadge')}
                        </span>
                      )}
                    </div>
                  </InfoRow>
                )}

                {ramGB && (
                  <InfoRow
                    label={t('widgets.sidebars.node.panel.infoRows.ram')}
                  >
                    {t('widgets.sidebars.node.panel.infoRows.ramValue', {
                      value: ramGB,
                    })}
                  </InfoRow>
                )}

                {osTagText && (
                  // In recovery this is the BOOTSTRAP's OS, not the node's
                  // installed one. Literally true either way, which is why it
                  // misleads: a reader takes the OS row for what the machine
                  // runs day to day. Say which one it is.
                  <InfoRow
                    label={
                      inRecovery
                        ? t('widgets.sidebars.node.panel.infoRows.osRecovery')
                        : t('widgets.sidebars.node.panel.infoRows.os')
                    }
                  >
                    <span
                      className="block truncate"
                      title={
                        inRecovery
                          ? t(
                              'widgets.sidebars.node.panel.infoRows.osRecoveryTooltip',
                              {
                                os: osTagText,
                              },
                            )
                          : osTagText
                      }
                    >
                      {osTagText}
                    </span>
                  </InfoRow>
                )}

                {storageLines.length > 0 && (
                  /* One list, every medium, largest first. The USB row that used
                     to sit beside this filed a node's actual system disk under
                     "USB" whenever it booted from one — see buildStorageLines.
                     The marker says which device the machine is running on now,
                     and is absent (never guessed) when that is unknowable. */
                  <InfoRow
                    label={t('widgets.sidebars.node.panel.infoRows.storage')}
                    align="start"
                  >
                    <div className="flex flex-col gap-0.5">
                      {storageLines.map((d) => (
                        <span
                          key={d.path}
                          className="flex items-center gap-1.5 truncate"
                          title={[d.path, d.model].filter(Boolean).join(' — ')}
                        >
                          {d.isBoot && (
                            <span
                              aria-label={t(
                                'widgets.sidebars.node.panel.infoRows.bootDeviceAria',
                              )}
                              title={t(
                                'widgets.sidebars.node.panel.infoRows.bootDeviceTitle',
                              )}
                              className="flex-shrink-0 text-green-6"
                            >
                              &#9679;
                            </span>
                          )}
                          <span className="truncate">
                            {[d.media, d.size].filter(Boolean).join(' · ')}
                          </span>
                        </span>
                      ))}
                    </div>
                  </InfoRow>
                )}

                {/* Shown only for an overlay root: the persistent case is the
                    norm and would be noise, while overlay is the one fact that
                    explains a 409 on every apply/install before it happens. */}
                {node?.system_info?.hardware?.boot?.root_persistence ===
                  'overlay' && (
                  <InfoRow
                    label={t(
                      'widgets.sidebars.node.panel.infoRows.rootFilesystem',
                    )}
                  >
                    <span
                      className="text-amber-4"
                      title={t(
                        'widgets.sidebars.node.panel.infoRows.rootOverlayTitle',
                      )}
                    >
                      {t('widgets.sidebars.node.panel.infoRows.rootOverlay')}
                    </span>
                  </InfoRow>
                )}

                {powerLine && (
                  <InfoRow
                    label={t('widgets.sidebars.node.panel.infoRows.power')}
                  >
                    <span
                      title={powerLine.title}
                      className={clsx(
                        powerLine.tone === 'bad' && 'text-red-6',
                        powerLine.tone === 'warn' && 'text-amber-6',
                        powerLine.tone === 'ok' && 'text-neutral-3',
                      )}
                    >
                      {powerLine.text}
                    </span>
                  </InfoRow>
                )}

                {usbDevices.length > 0 && (
                  /* Attached USB devices that are NOT storage — a disk appears
                     in Storage above, and listing it twice would invite an
                     operator to reason about two records for one object. */
                  <InfoRow
                    label={t('widgets.sidebars.node.panel.infoRows.usb')}
                    align="start"
                  >
                    <div className="flex flex-col gap-0.5">
                      {usbDevices.map((d) => (
                        <span
                          key={`${d.port}-${d.vendor_id}-${d.product_id}`}
                          className="block truncate"
                          title={[
                            `${d.vendor_id}:${d.product_id}`,
                            d.port
                              ? t(
                                  'widgets.sidebars.node.panel.infoRows.usbPort',
                                  { port: d.port },
                                )
                              : null,
                            d.speed
                              ? t(
                                  'widgets.sidebars.node.panel.infoRows.usbSpeed',
                                  { speed: d.speed },
                                )
                              : null,
                          ]
                            .filter(Boolean)
                            .join(' · ')}
                        >
                          {usbDeviceLine(d)}
                        </span>
                      ))}
                    </div>
                  </InfoRow>
                )}

                {tpmText && (
                  <InfoRow
                    label={t('widgets.sidebars.node.panel.infoRows.tpm')}
                  >
                    {tpmText}
                  </InfoRow>
                )}

                {node?.system_info?.hardware?.hat && (
                  /* Spec-compliant HAT ID EEPROM — detected, not declared,
                     which is why it lives with the hardware facts. */
                  <InfoRow
                    label={t('widgets.sidebars.node.panel.infoRows.hat')}
                  >
                    <span
                      className="block truncate"
                      title={[
                        node.system_info.hardware.hat.vendor,
                        node.system_info.hardware.hat.product_id,
                        node.system_info.hardware.hat.uuid,
                      ]
                        .filter(Boolean)
                        .join(' · ')}
                    >
                      {node.system_info.hardware.hat.product ||
                        node.system_info.hardware.hat.vendor ||
                        t('widgets.sidebars.node.panel.infoRows.hatDetected')}
                    </span>
                  </InfoRow>
                )}
              </div>
            )}

            <NodeNotesSection
              nodeId={nodeId}
              notes={node.notes}
              editable={check(Actions.NodeUpdate, ou)}
            />

            <MetricsPanel nodeId={nodeId} online={node.status === 'online'} />
          </>
        )}

        {showTabs && activeTab === 'actions' && (
          <div className="relative overflow-hidden">
            {/* Action list. When a sub-view opens it slides left + fades out;
                the sub-view slides in from the right in its place — all within
                the node panel, so its header and tabs stay put. */}
            <div
              className={clsx(
                'flex flex-col gap-3 transition-all duration-300',
                actionView
                  ? 'pointer-events-none absolute inset-0 -translate-x-8 opacity-0'
                  : 'translate-x-0 opacity-100',
              )}
            >
              {/* Terminal — most-reached, sits alone at the top. */}
              {caps.terminal && check(Actions.NodeTerminal, ou) && (
                <>
                  <div className="flex flex-col">
                    <ActionRow
                      icon={<IconTerminal />}
                      label={t('widgets.sidebars.node.panel.actions.terminal')}
                      onClick={() => setActionView('terminal')}
                      disabled={node.status !== 'online'}
                      title={
                        node.status === 'online'
                          ? t(
                              'widgets.sidebars.node.panel.actions.terminalTitle',
                            )
                          : t(
                              'widgets.sidebars.node.panel.actions.nodeMustBeOnline',
                            )
                      }
                    />
                  </div>
                  <div className="h-px bg-neutral-11" />
                </>
              )}

              {/* Management / info group. */}
              <div className="flex flex-col">
                <ActionRow
                  icon={<IconHistory />}
                  label={t('widgets.sidebars.node.panel.header.commandHistory')}
                  onClick={() => setActionView('history')}
                />
                {/* Hostname editing — hidden while netbooted (recovery hostname
                    is ephemeral). Opens as a sub-view with a propagation note. */}
                {!inMaintenance && check(Actions.NodeSetHostname, ou) && (
                  <ActionRow
                    icon={<IconHostname />}
                    label={t(
                      'widgets.sidebars.node.panel.actions.changeHostname',
                    )}
                    onClick={() => setActionView('hostname')}
                  />
                )}
                {check(Actions.PlaybookRun, ou) && (
                  <ActionRow
                    icon={<IconPlaybook />}
                    label={t('widgets.sidebars.runPlaybook.title')}
                    onClick={() => setActionView('runPlaybook')}
                  />
                )}
              </div>

              {/* Reboot group. */}
              {(check(Actions.NodeReboot, ou) ||
                (caps.netboot && check(Actions.NodeRebootRecovery, ou))) && (
                <>
                  <div className="h-px bg-neutral-11" />
                  <div className="flex flex-col">
                    {check(Actions.NodeReboot, ou) && (
                      <ActionRow
                        icon={<IconReboot />}
                        label={t('widgets.sidebars.node.panel.actions.reboot')}
                        onClick={() => setActionView('confirmReboot')}
                      />
                    )}
                    {caps.netboot && check(Actions.NodeRebootRecovery, ou) && (
                      <ActionRow
                        icon={<IconReboot />}
                        label={t(
                          'widgets.sidebars.node.panel.actions.rebootToRecovery',
                        )}
                        onClick={() => setActionView('confirmRecovery')}
                        disabled={inMaintenance || bootHostMismatch}
                        title={
                          bootHostMismatch
                            ? t(
                                'widgets.sidebars.node.panel.actions.recoveryMismatchTitle',
                              )
                            : inMaintenance
                              ? t(
                                  'widgets.sidebars.node.panel.actions.recoveryNetbootedTitle',
                                )
                              : t(
                                  'widgets.sidebars.node.panel.actions.recoveryDefaultTitle',
                                )
                        }
                      />
                    )}
                  </div>
                </>
              )}

              {/* Provisioning — disk-level operations that must run from the
                  netboot recovery bootstrap. On a managed OS they would target
                  the running disk and are refused by the agent, so the whole
                  section is shown only while the node is netbooted. */}
              {inMaintenance &&
                (check(Actions.NodeInstallOS, ou) ||
                  check(Actions.NodeUpdateAgent, ou) ||
                  check(Actions.NodeFileWrite, ou)) && (
                  <>
                    <SectionDivider
                      label={t(
                        'widgets.sidebars.node.panel.actions.provisioning',
                      )}
                    />
                    <div className="flex flex-col">
                      {check(Actions.NodeInstallOS, ou) && (
                        <ActionRow
                          icon={<IconOS />}
                          label={t('widgets.sidebars.installOS.defaultHeading')}
                          onClick={() => setActionView('installOS')}
                        />
                      )}
                      {check(Actions.NodeInstallOS, ou) && (
                        <ActionRow
                          icon={<IconRestore />}
                          label={t(
                            'widgets.sidebars.node.panel.actions.restoreFromBackup',
                          )}
                          onClick={() => setActionView('restore')}
                          title={t(
                            'widgets.sidebars.node.panel.actions.restoreTitle',
                          )}
                        />
                      )}
                      {check(Actions.NodeInstallOS, ou) && (
                        <ActionRow
                          icon={<IconBackup />}
                          label={t(
                            'widgets.sidebars.node.panel.actions.createBackup',
                          )}
                          onClick={() => setActionView('captureImage')}
                          title={t(
                            'widgets.sidebars.node.panel.actions.createBackupTitle',
                          )}
                        />
                      )}
                      {check(Actions.NodeUpdateAgent, ou) && (
                        <ActionRow
                          icon={<IconAgent />}
                          label={t(
                            'widgets.sidebars.node.panel.actions.installAgent',
                          )}
                          onClick={() => setActionView('installAgent')}
                        />
                      )}
                      {check(Actions.AccessManage, ou) && (
                        <ActionRow
                          icon={<IconLink />}
                          label={t(
                            'widgets.sidebars.node.panel.actions.provisionAccess',
                          )}
                          onClick={() => setActionView('provisionConnection')}
                        />
                      )}
                    </div>
                  </>
                )}

              {/* Danger zone — red section header. */}
              {(check(Actions.NodeReplace, ou) ||
                check(Actions.NodeShutdown, ou) ||
                check(Actions.NodeDelete, ou)) && (
                <>
                  <SectionDivider
                    label={t('widgets.sidebars.node.panel.actions.danger')}
                    tone="danger"
                  />
                  <div className="flex flex-col">
                    {check(Actions.NodeReplace, ou) && (
                      <ActionRow
                        icon={<IconReplace />}
                        label={t(
                          'widgets.sidebars.node.panel.actions.replaceNode',
                        )}
                        onClick={() => setActionView('replace')}
                      />
                    )}
                    {check(Actions.NodeShutdown, ou) && (
                      <ActionRow
                        icon={<IconPower />}
                        label={t(
                          'widgets.sidebars.node.panel.actions.shutdown',
                        )}
                        danger
                        onClick={() => setActionView('confirmShutdown')}
                      />
                    )}
                    {check(Actions.NodeDelete, ou) && (
                      <ActionRow
                        icon={<IconTrash />}
                        label={t(
                          'widgets.sidebars.node.panel.actions.deleteNode',
                        )}
                        danger
                        onClick={() => setActionView('confirmDelete')}
                      />
                    )}
                  </div>
                </>
              )}
            </div>

            {/* Inline sub-view (slides in from the right). */}
            {actionView && (
              <div
                className={clsx(
                  'transition-all duration-300',
                  subEnter
                    ? 'translate-x-0 opacity-100'
                    : 'translate-x-8 opacity-0',
                )}
              >
                {actionView === 'terminal' && (
                  <TerminalPanel
                    nodeId={nodeId}
                    online={node.status === 'online'}
                    embedded
                    onBack={closeActionView}
                  />
                )}
                {actionView === 'hostname' && (
                  <HostnameControl
                    nodeId={nodeId}
                    hostname={node.hostname}
                    online={node.status === 'online'}
                    canRefreshDhcp={check(Actions.NodeFileWrite, ou)}
                    onBack={closeActionView}
                  />
                )}
                {actionView === 'confirmReboot' && (
                  <ActionConfirm
                    title={t('widgets.sidebars.node.panel.actions.reboot')}
                    message={t(
                      'widgets.sidebars.node.panel.confirms.rebootMessage',
                    )}
                    confirmText={t(
                      'widgets.sidebars.node.panel.actions.reboot',
                    )}
                    onConfirm={() => {
                      handleReboot()
                      // Jump to history so the reboot progress is right there.
                      setActionView('history')
                    }}
                    onBack={closeActionView}
                  />
                )}
                {actionView === 'confirmShutdown' && (
                  <ActionConfirm
                    title={t('widgets.sidebars.node.panel.actions.shutdown')}
                    message={t(
                      'widgets.sidebars.node.panel.confirms.shutdownMessage',
                    )}
                    confirmText={t(
                      'widgets.sidebars.node.panel.actions.shutdown',
                    )}
                    destructive
                    onConfirm={() => {
                      handleShutdown()
                      setActionView(null)
                    }}
                    onBack={closeActionView}
                  />
                )}
                {actionView === 'confirmRecovery' && (
                  <ActionConfirm
                    title={t(
                      'widgets.sidebars.node.panel.actions.rebootToRecovery',
                    )}
                    message={t(
                      'widgets.sidebars.node.panel.confirms.recoveryMessage',
                    )}
                    confirmText={t(
                      'widgets.sidebars.node.panel.actions.rebootToRecovery',
                    )}
                    confirmClassName="border !border-violet-5 !bg-transparent !text-violet-5 hover:!bg-violet-5/10"
                    note={<SourceOfferNote subject="image" />}
                    onConfirm={() => {
                      handleRebootToRecovery()
                      // Jump to history so the reboot progress is right there.
                      setActionView('history')
                    }}
                    onBack={closeActionView}
                  />
                )}
                {actionView === 'confirmDelete' && (
                  <ActionConfirm
                    title={t('widgets.sidebars.node.panel.actions.deleteNode')}
                    message={
                      isNodeInConnectedState(node?.status)
                        ? t('widgets.sidebars.node.panel.deleteOnlineWarning')
                        : t('widgets.sidebars.node.panel.deleteWarning')
                    }
                    confirmText={t(
                      'widgets.sidebars.node.panel.confirms.delete',
                    )}
                    destructive
                    onConfirm={handleDelete}
                    onBack={closeActionView}
                  />
                )}
                {actionView === 'replace' && (
                  <ReplaceNodeControl
                    nodeId={nodeId}
                    embedded
                    onBack={closeActionView}
                  />
                )}
                {(actionView === 'installOS' || actionView === 'restore') && (
                  <SidebarInstallOS
                    nodeId={nodeId}
                    heading={
                      actionView === 'restore'
                        ? t(
                            'widgets.sidebars.node.panel.actions.restoreFromBackup',
                          )
                        : t('widgets.sidebars.installOS.defaultHeading')
                    }
                    preselectDefault={actionView === 'installOS'}
                    onDone={closeActionView}
                    onClose={onClose}
                  />
                )}
                {actionView === 'captureImage' && (
                  <SidebarCaptureImage
                    nodeId={nodeId}
                    onDone={closeActionView}
                    onClose={onClose}
                  />
                )}
                {actionView === 'runPlaybook' && (
                  <SidebarRunPlaybook
                    nodeId={nodeId}
                    onDone={closeActionView}
                    onClose={onClose}
                  />
                )}
                {actionView === 'installAgent' && (
                  <SidebarInstallAgent
                    nodeId={nodeId}
                    onDone={closeActionView}
                    onClose={onClose}
                  />
                )}
                {actionView === 'provisionConnection' && (
                  <SidebarProvisionConnection
                    nodeId={nodeId}
                    onDone={closeActionView}
                    onClose={onClose}
                  />
                )}
                {actionView === 'history' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t(
                        'widgets.sidebars.node.panel.header.commandHistory',
                      )}
                      onBack={closeActionView}
                    />
                    <CommandHistory commands={commands} />
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {/* Properties: Boot behavior inline at the top, then a list of rows
            that open right slide-in sub-views (same pattern as Actions) —
            Edit node info, Credentials, Secrets, and Attributes at the bottom.
            Each row gates itself; Attributes is read-first (always shown). */}
        {showTabs && activeTab === 'properties' && (
          <div className="relative overflow-hidden">
            <div
              className={clsx(
                'flex flex-col gap-6 transition-all duration-300',
                actionView
                  ? 'pointer-events-none absolute inset-0 -translate-x-8 opacity-0'
                  : 'translate-x-0 opacity-100',
              )}
            >
              {/* Node name — inline editor at the very top, above Boot
                  behavior. Replaces the old "Edit node info" form. */}
              {check(Actions.NodeUpdate, ou) && (
                <div className="flex flex-col gap-2">
                  <SectionDivider
                    label={t('widgets.sidebars.node.panel.properties.name')}
                  />
                  <NodeNameControl nodeId={nodeId} name={node.name} />
                </div>
              )}

              {check(Actions.NodeAssignOU) && <NodeOUSection nodeId={nodeId} />}

              <div className="flex flex-col">
                {canPolicy && (
                  <>
                    <ActionRow
                      icon={<IconKey />}
                      label={t(
                        'widgets.sidebars.node.panel.properties.credentials',
                      )}
                      onClick={() => setActionView('credentials')}
                      title={t(
                        'widgets.sidebars.node.panel.properties.credentialsTitle',
                      )}
                    />
                    {/* The secrets that reach a node are served on
                        secret.read, account-wide. */}
                    {check(Actions.SecretRead) && (
                      <ActionRow
                        icon={<IconLock />}
                        label={t(
                          'widgets.sidebars.node.panel.properties.secrets',
                        )}
                        onClick={() => setActionView('secrets')}
                        title={t(
                          'widgets.sidebars.node.panel.properties.secretsTitle',
                        )}
                      />
                    )}
                  </>
                )}
                <ActionRow
                  icon={<IconTag />}
                  label={t('widgets.sidebars.node.panel.properties.attributes')}
                  onClick={() => setActionView('attributes')}
                  title={t(
                    'widgets.sidebars.node.panel.properties.attributesTitle',
                  )}
                />
                {/* Hardware platform declaration (carrier board / variant /
                    modules) + its apply/scan ladder — its own sub-view, like
                    Credentials and Secrets. */}
                {isRaspberryPi && (
                  <ActionRow
                    icon={<IconWrench />}
                    label={t('widgets.sidebars.node.panel.properties.platform')}
                    title={t(
                      'widgets.sidebars.node.panel.properties.platformTitle',
                    )}
                    onClick={() => setActionView('platform')}
                  />
                )}
                {/* Watcher (Watcher) nodes are refused every command, so
                    offering LED controls there only manufactures 409s. */}
                {isRaspberryPi && node.agent_kind !== 'watcher' && (
                  <ActionRow
                    icon={<IconWrench />}
                    label={t('widgets.sidebars.node.panel.properties.leds')}
                    title={t(
                      'widgets.sidebars.node.panel.properties.ledsTitle',
                    )}
                    onClick={() => setActionView('leds')}
                  />
                )}
                {check(Actions.UtilityRead, ou) && (
                  <ActionRow
                    icon={<IconWrench />}
                    label={t(
                      'widgets.sidebars.node.panel.properties.utilities',
                    )}
                    onClick={() => setActionView('utilities')}
                    title={t(
                      'widgets.sidebars.node.panel.properties.utilitiesTitle',
                    )}
                  />
                )}
              </div>
            </div>

            {actionView && (
              <div
                className={clsx(
                  'transition-all duration-300',
                  subEnter
                    ? 'translate-x-0 opacity-100'
                    : 'translate-x-8 opacity-0',
                )}
              >
                {actionView === 'credentials' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t(
                        'widgets.sidebars.node.panel.properties.credentials',
                      )}
                      onBack={closeActionView}
                    />
                    <NodeAccessSection nodeId={nodeId} />
                  </div>
                )}
                {actionView === 'secrets' && check(Actions.SecretRead) && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t(
                        'widgets.sidebars.node.panel.properties.secrets',
                      )}
                      onBack={closeActionView}
                    />
                    <NodeSecretsSection
                      nodeId={nodeId}
                      // The attachment routes check the right account-wide,
                      // so a unit-scoped grant is offered nothing it would
                      // only be refused.
                      canManage={check(Actions.SecretManage)}
                    />
                  </div>
                )}
                {actionView === 'attributes' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t(
                        'widgets.sidebars.node.panel.properties.attributes',
                      )}
                      onBack={closeActionView}
                      // The reference syntax is passed as an interpolation VALUE
                      // so the literal {{...}} token renders instead of being
                      // swallowed by i18next (values are not re-interpolated).
                      hint={t(
                        'widgets.sidebars.node.panel.properties.attributesHint',
                        {
                          placeholder: '{{node.attributes.<key>}}',
                        },
                      )}
                    />
                    <NodeAttributesSection
                      nodeId={nodeId}
                      hostname={node.hostname}
                      editable={check(Actions.SecretManage, ou)}
                    />
                  </div>
                )}
                {actionView === 'utilities' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t(
                        'widgets.sidebars.node.panel.properties.utilities',
                      )}
                      onBack={closeActionView}
                      hint={t(
                        'widgets.sidebars.node.panel.properties.utilitiesHint',
                      )}
                    />
                    <NodeUtilitiesSection
                      nodeId={nodeId}
                      canManage={check(Actions.UtilityManage, ou)}
                    />
                  </div>
                )}
                {actionView === 'leds' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t('widgets.sidebars.node.panel.properties.leds')}
                      onBack={closeActionView}
                    />
                    <LEDSection
                      nodeId={nodeId}
                      canManage={check(Actions.NodeUpdate, ou)}
                      online={node.status === 'online'}
                      agentVersion={node.agent_version}
                    />
                  </div>
                )}
                {actionView === 'platform' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t(
                        'widgets.sidebars.node.panel.properties.platform',
                      )}
                      onBack={closeActionView}
                      hint={t(
                        'widgets.sidebars.node.panel.properties.platformHint',
                      )}
                    />
                    <DevicePlatformSection
                      nodeId={nodeId}
                      canEdit={check(Actions.NodeFileWrite, ou)}
                      canScan={check(Actions.NodeStatusCheck, ou)}
                      canReboot={check(Actions.NodeReboot, ou)}
                      online={node.status === 'online'}
                      lastBootAt={node.last_boot_at}
                    />
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {/* Advanced: a list of rows that open right slide-in sub-views (same
            pattern as Actions). Order: Watchdog, Config files, Storage, Boot
            order, Bootloader, then Re-adopt at the bottom. Each row gates
            itself; the sub-view headers are the shared SubViewHeader. */}
        {showTabs && activeTab === 'advanced' && (
          <div className="relative overflow-hidden">
            <div
              className={clsx(
                'flex flex-col transition-all duration-300',
                actionView
                  ? 'pointer-events-none absolute inset-0 -translate-x-8 opacity-0'
                  : 'translate-x-0 opacity-100',
              )}
            >
              {/* Watchdog targets the installed OS, so it is hidden while the
                  node is netbooted for maintenance. */}
              {isRaspberryPi && !inMaintenance && (
                <ActionRow
                  icon={<IconWatchdog />}
                  label={t('widgets.sidebars.node.panel.advanced.watchdog')}
                  onClick={() => setActionView('watchdog')}
                />
              )}
              {/* Overclock edits the installed OS's config.txt, so like the
                  watchdog it is hidden while the node is netbooted. */}
              {isRaspberryPi && !inMaintenance && (
                <ActionRow
                  icon={<IconGauge />}
                  label={t('widgets.sidebars.node.panel.advanced.overclock')}
                  onClick={() => setActionView('overclock')}
                />
              )}
              <ActionRow
                icon={<IconFile />}
                label={t('widgets.sidebars.node.panel.advanced.files')}
                onClick={() => setActionView('files')}
              />
              {hasStorageDevices && (
                <ActionRow
                  icon={<IconStorage />}
                  label={t('widgets.sidebars.node.panel.infoRows.storage')}
                  onClick={() => setActionView('storage')}
                />
              )}
              {/* Bootloader/EEPROM needs the signer service; on an install
                  that runs none (caps.eeprom_signer), hide the action rather
                  than offer 500s. */}
              {isRaspberryPi && caps.eeprom_signer && (
                <ActionRow
                  icon={<IconChip />}
                  label={t(
                    'widgets.sidebars.node.panel.advanced.bootloaderEeprom',
                  )}
                  onClick={() => setActionView('bootloader')}
                />
              )}
              {/* Re-adopt — provisioning artifacts for rebinding this node to
                  its hardware; least routine, so it sits at the bottom. */}
              {check(Actions.NodeCredentials, ou) && (
                <ActionRow
                  icon={<IconDownload />}
                  label={t('widgets.sidebars.node.panel.advanced.readopt')}
                  onClick={() => setActionView('readopt')}
                />
              )}
            </div>

            {actionView && (
              <div
                className={clsx(
                  'transition-all duration-300',
                  subEnter
                    ? 'translate-x-0 opacity-100'
                    : 'translate-x-8 opacity-0',
                )}
              >
                {actionView === 'watchdog' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t('widgets.sidebars.node.panel.advanced.watchdog')}
                      onBack={closeActionView}
                      hint={t(
                        'widgets.sidebars.node.panel.advanced.watchdogHint',
                      )}
                    />
                    <WatchdogSection
                      nodeId={nodeId}
                      canManage={check(Actions.NodeFileWrite, ou)}
                      online={node.status === 'online'}
                      agentVersion={node.agent_version}
                      // A disarmed breaker leaves a stable but unsupervised node
                      // with the evidence still on its disk; recovery is where
                      // that can be read. The gate is the same one the Reboot
                      // group uses — an install without netboot, a bootloader
                      // pointing elsewhere, or a node already in recovery has no
                      // such path, so the button simply is not offered.
                      onSendToRecovery={
                        caps.netboot &&
                        check(Actions.NodeRebootRecovery, ou) &&
                        !inMaintenance &&
                        !bootHostMismatch
                          ? // openActionView, not setActionView: 'confirmRecovery'
                            // is hosted by the Actions tab and this button lives
                            // in Advanced, so setting the view directly hid the
                            // action list and rendered an empty panel with no way
                            // back — and never dispatched the reboot.
                            () => openActionView('confirmRecovery')
                          : undefined
                      }
                    />
                  </div>
                )}
                {actionView === 'overclock' && (
                  <OverclockControl
                    nodeId={nodeId}
                    overclock={overclock}
                    online={node.status === 'online'}
                    nodeBusy={nodeIsBusy(node.status)}
                    canApply={check(Actions.NodeFileWrite, ou)}
                    onBack={closeActionView}
                  />
                )}
                {/* Config files is a two-step sub-view: the file list, then the
                    editor for one file. Both stay inside the Advanced tab —
                    picking a file used to replace the whole node panel (its own
                    header + the legacy actions menu), which lost the tabs and
                    the node context. Back from the editor returns to the list. */}
                {actionView === 'files' && !editingFile && (
                  <NodeFilesSection
                    nodeId={nodeId}
                    canWrite={check(Actions.NodeFileWrite, ou)}
                    online={node.status === 'online'}
                    inMaintenance={inMaintenance}
                    onOpen={(file) => setEditingFile(file)}
                    onBack={closeActionView}
                  />
                )}
                {actionView === 'files' && editingFile && (
                  <NodeFileEditor
                    nodeId={nodeId}
                    file={editingFile}
                    canWrite={check(Actions.NodeFileWrite, ou)}
                    online={node.status === 'online'}
                    inMaintenance={inMaintenance}
                    onBack={() => setEditingFile(null)}
                    onGone={() => setEditingFile(null)}
                  />
                )}
                {/* Storage is a two-step sub-view like Config files: the drive
                    list, then one drive's details (including Erase). */}
                {actionView === 'storage' && !selectedStorageDevice && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t('widgets.sidebars.node.panel.infoRows.storage')}
                      onBack={closeActionView}
                    />
                    <div className="flex flex-col">
                      {Object.entries(storageDevices).map(
                        ([devicePath, deviceInfo]) => (
                          <StorageDeviceListItem
                            key={devicePath}
                            devicePath={devicePath}
                            deviceInfo={deviceInfo}
                            onSelect={setStorageDevicePath}
                          />
                        ),
                      )}
                    </div>
                  </div>
                )}
                {actionView === 'storage' &&
                  selectedStorageDevice &&
                  storageDevicePath && (
                    <StorageDeviceSection
                      nodeId={nodeId}
                      devicePath={storageDevicePath}
                      device={selectedStorageDevice}
                      canErase={check(Actions.NodeInstallOS, ou)}
                      onBack={() => setStorageDevicePath(null)}
                    />
                  )}
                {actionView === 'bootloader' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t(
                        'widgets.sidebars.node.panel.advanced.bootloaderEeprom',
                      )}
                      onBack={closeActionView}
                    />
                    <BootloaderSection
                      nodeId={nodeId}
                      // The server accepts a command for an online OR a
                      // maintenance node; asking for "online" here was stricter
                      // than the dispatch guard for no reason, and it left the
                      // one control that finishes a flash dead while the
                      // platform was still settling the flash itself. The other
                      // panels keep their own gate for now — widening them is a
                      // separate call, since several would then let an operator
                      // stack a second dispatch behind one in flight.
                      canDispatch={isNodeDispatchable(node.status)}
                      canFlashEEPROM={check(Actions.NodeEEPROMFlash, ou)}
                      bootloaderVersion={node.bootloader_version}
                      bootloaderTimestamp={node.bootloader_timestamp}
                      bootOrder={node?.boot_order}
                      cpuModel={node.system_info?.hardware?.cpu?.model}
                      currentBootDevice={
                        node.system_info?.hardware?.boot?.current_boot_device
                      }
                      inRecovery={inRecovery}
                      stagedAt={stagedBootloaderAt}
                      rebootStartedAt={inFlightReboot?.created_at}
                      storageDevicePaths={Object.keys(
                        node.system_info?.storage_devices ||
                          node.storage_devices ||
                          {},
                      )}
                      eepromEnabled={
                        node.system_info?.hardware?.eeprom?.enabled
                      }
                      eepromDescription={
                        node.system_info?.hardware?.eeprom?.description
                      }
                    />
                  </div>
                )}
                {actionView === 'readopt' && (
                  <div className="flex flex-col gap-4">
                    <SubViewHeader
                      title={t('widgets.sidebars.node.panel.advanced.readopt')}
                      onBack={closeActionView}
                    />
                    <div className="text-xs text-neutral-6">
                      {t('widgets.sidebars.node.panel.advanced.readoptBody')}
                    </div>
                    <div className="flex flex-col">
                      {caps.sd_provisioning && (
                        <ActionRow
                          icon={<IconDownload />}
                          label={t(
                            'widgets.sidebars.node.panel.adoption.downloadSdImage',
                          )}
                          onClick={handleDownloadNodeSDImage}
                        />
                      )}
                      <ActionRow
                        icon={<IconDownload />}
                        label={t(
                          'widgets.sidebars.node.panel.adoption.downloadAdoptionFile',
                        )}
                        onClick={handleDownloadAdoptionFile}
                      />
                    </div>
                    {caps.sd_provisioning && (
                      <SourceOfferNote subject="bundle" />
                    )}
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {activeTab === 'history' && <CommandHistory commands={commands} />}
      </div>
      <ConfirmDialog
        open={pendingConfirm === 'delete'}
        title={t('widgets.sidebars.node.panel.actions.deleteNode')}
        message={
          isNodeInConnectedState(node?.status)
            ? t('widgets.sidebars.node.panel.deleteOnlineWarning')
            : t('widgets.sidebars.node.panel.deleteWarning')
        }
        confirmText={t('widgets.sidebars.node.panel.confirms.delete')}
        destructive
        onConfirm={handleDelete}
        onCancel={() => setPendingConfirm(null)}
      />
      <ConfirmDialog
        open={pendingConfirm === 'reject'}
        title={t('widgets.sidebars.node.panel.confirms.rejectTitle')}
        message={t('widgets.sidebars.node.panel.confirms.rejectMessage')}
        confirmText={t('widgets.sidebars.node.panel.banners.reject')}
        destructive
        onConfirm={handleReject}
        onCancel={() => setPendingConfirm(null)}
      />
    </div>
  )
}

// InstallCommandBlock renders the one-command installer for a specific node:
// it embeds this node's adoption token, so the device that runs it becomes
// exactly this node (pre-approved). Works on any deployment profile — the
// agent connects outbound over gRPC-TLS.
const InstallCommandBlock: FC<{ nodeId: string }> = ({ nodeId }) => {
  const { t } = useTranslation()
  const [minted, setMinted] = useState<{
    command: string
    expiresAt: string
  } | null>(null)
  const [minting, setMinting] = useState(false)
  const { state: copyState, copy } = useCopyText()

  // Minted on an explicit click, not on every mount: a code is single-use,
  // and minting one revokes the node's previous code, so opening this panel
  // (or a colleague opening it) silently broke a command copied earlier. The
  // link carries the opaque code, not the adoption blob (no account
  // id/topology or reusable credential leaked).
  const mint = async () => {
    setMinting(true)
    try {
      const res = await apiNodes.createAdoptionCode(nodeId)
      setMinted({ command: res.install_command, expiresAt: res.expires_at })
    } catch (err) {
      showError(t('widgets.sidebars.node.panel.adoption.mintError'), err)
    } finally {
      setMinting(false)
    }
  }

  if (!minted) {
    return (
      <div className="flex flex-col gap-2">
        <div className="text-sm text-neutral-4">
          {t('widgets.sidebars.node.panel.adoption.installIntro')}
        </div>
        <Button
          size="md"
          kind="secondary"
          onClick={() => void mint()}
          disabled={minting}
          grow
        >
          {t('widgets.sidebars.node.panel.adoption.showCommand')}
        </Button>
        <div className="text-xs text-neutral-6">
          {t('widgets.sidebars.node.panel.adoption.replacesEarlier')}
        </div>
      </div>
    )
  }
  const command = minted.command

  const handleCopy = () => {
    void copy(command)
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.node.panel.adoption.installIntro')}
      </div>
      <pre className="bg-neutral-13 rounded px-3 py-2 text-xs text-neutral-3 font-mono whitespace-pre-wrap break-all select-all">
        {command}
      </pre>
      <div className="text-xs text-neutral-6">
        {t('widgets.sidebars.node.panel.adoption.singleUse', {
          time: new Date(minted.expiresAt).toLocaleString(),
        })}
      </div>
      <Button size="md" onClick={handleCopy} grow>
        {copyState === 'copied'
          ? t('widgets.sidebars.node.panel.adoption.copiedCheck')
          : copyState === 'failed'
            ? t('shared.copy.manual')
            : t('widgets.sidebars.node.panel.adoption.copyCommand')}
      </Button>
    </div>
  )
}
