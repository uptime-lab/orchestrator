import clsx from 'clsx'
import { createLocator, Locator, Mark } from 'create-locator'
import { useUnit } from 'effector-react'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { identifyNode } from '@/entities/deviceProfile'
import {
  $buttonPressedNodes,
  buttonMarkCleared,
} from '@/entities/node/buttonPulse'
import {
  $identifyBlinkNodes,
  formatBlink,
  identifyBlinkStarted,
  identifyBlinkStopped,
} from '@/entities/node/identifyBlink'
import {
  toggleFavoriteFx,
  useFavoriteNodeIds,
  useNodeMetrics,
} from '@/entities/node/model'
import { nodePowerLine } from '@/entities/node/power.ts'
import { NodeData } from '@/entities/node/ui/nodeList'
import {
  NodeColumn,
  nodeColumnsOrdered,
} from '@/entities/node/ui/nodeList/nodeColumns'
import {
  isNodeHeldForWork,
  nodeDisplayName,
  NodeDisplayState,
  nodeDisplayState,
  nodeStatusText,
  normalizeStatus,
} from '@/entities/node/utils.ts'
import { useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { usePreferHostnames } from '@/entities/user/preferences'

import { useUptime } from '@/shared/hooks/useUptime'
import { showError, showSuccess } from '@/shared/ui/toast'

// dataTransfer key for dragging a node card onto the OU rail.
export const NODE_DND_TYPE = 'text/node-id'

export type NodeCardLocator = Locator<{
  id: { id: string }
  name: { name: string }
  ip: { ip: string }
}>

type NodeCardProps = {
  id: string
  nodeInfo: NodeData
  selected: boolean
  onClick?: (id: string) => unknown
  isMultiselectMode?: boolean
  isSelected?: boolean
  onSelect?: (id: string) => unknown
  // Whether the row may be dragged onto an OU. Off in the plain list; on while
  // selecting (multiselect) or editing units.
  dragEnabled?: boolean
  // Node ids this card drags when assigned to an OU. Normally just [id]; in
  // multiselect, when this card is part of the selection, the whole selection.
  dragIds?: string[]
  // True while a GROUP drag is in flight and this card is part of it. The card
  // that is physically under the cursor knows it is dragging on its own; every
  // other selected row does not, and a multiselect drag that dims one row while
  // moving twelve is confusing.
  inGroupDrag?: boolean
  // Announces the size of the drag this card started, so the list can mark the
  // rest of the selection. 0 means the drag ended.
  onGroupDragChange?: (count: number) => void
  // Visible metric columns (user-hidden ones filtered out by NodeList).
  columns?: NodeColumn[]
} & Mark<NodeCardLocator>

const GB = 1024 * 1024 * 1024

// Neon-tube status glyph: a slim vertical bar at the row's left edge. Lit
// states glow; waiting states pulse; dead nodes stay dim.
//
// Raw values rather than Tailwind classes, because the identify blink
// alternates between the lit blue and THIS, and a keyframe cannot be handed a
// class. One map, so the resting look and the blink's resting half cannot
// drift apart.
//
// `deleted` is not a node state (nodeDisplayState never returns it): it is the
// tube of a deleted node's row, a tombstone listed below the live fleet. Grey,
// no glow — the platform hears nothing from that device, and red would claim
// the fault is the device's, when it is the delete.
export type StatusTubeState = NodeDisplayState | 'deleted'

const tubeRest: Record<
  StatusTubeState,
  { color: string; glow: string; pulse: boolean }
> = {
  online: {
    color: '#40ff3d',
    glow: '0 0 10px rgba(64,255,61,0.8)',
    pulse: false,
  },
  recovery: {
    color: '#8e66ff',
    glow: '0 0 10px rgba(142,102,255,0.9)',
    pulse: true,
  },
  pending: {
    color: '#fff73d',
    glow: '0 0 10px rgba(255,247,61,0.6)',
    pulse: true,
  },
  offline: { color: 'rgba(145,35,35,0.7)', glow: 'none', pulse: false },
  other: { color: 'rgba(217,210,52,0.7)', glow: 'none', pulse: false },
  deleted: { color: 'rgba(120,120,120,0.55)', glow: 'none', pulse: false },
}

// The lit half of the blink: a saturated core plus a double halo. A neon tube
// is read by its glow, and the single 10px shadow the resting states carry is
// not enough to say "this is the node you are looking for" across a rack room.
const TUBE_LIT_COLOR = '#4b93ff'
const TUBE_LIT_GLOW =
  '0 0 6px 1px rgba(75,147,255,1), 0 0 18px 5px rgba(75,147,255,0.65)'

export const StatusTube: React.FC<{
  state: StatusTubeState
  label: string
  // While the device's locate LED blinks, the tube takes over as the blink:
  // it is the one row element visible at any list width and from across the
  // screen, which a 28px button at the identity column's edge is not. It
  // alternates with the status colour rather than replacing it, so an
  // identifying node still says online / recovery while it blinks.
  blinking: boolean
}> = ({ state, label, blinking }) => {
  const { t } = useTranslation()
  const rest = tubeRest[state]
  const identifyingLabel = blinking
    ? t('entities.nodeList.nodeCard.identifyingLabel', { label })
    : label
  return (
    <div
      role="img"
      aria-label={identifyingLabel}
      title={identifyingLabel}
      className={clsx(
        'w-1 h-10 rounded-full flex-shrink-0',
        blinking ? 'animate-identifyBlink' : rest.pulse && 'animate-pulse',
      )}
      style={
        blinking
          ? ({
              '--tube-lit': TUBE_LIT_COLOR,
              '--tube-lit-glow': TUBE_LIT_GLOW,
              '--tube-rest': rest.color,
              '--tube-rest-glow': rest.glow,
            } as React.CSSProperties)
          : { backgroundColor: rest.color, boxShadow: rest.glow }
      }
    />
  )
}

// Aggregate storage across all devices: total = sum of device capacities,
// used = sum of partition used_space (available only on a managed OS). Returns
// null when the node reports no storage at all.
const storageSummary = (
  node: NodeData,
): { totalGB: number; usedGB: number | null } | null => {
  const devices =
    node.system_info?.storage_devices ?? node.storage_devices ?? {}
  const entries = Object.values(devices)
  if (entries.length === 0) return null
  const total = entries.reduce((sum, d) => sum + (d.total_capacity || 0), 0)
  if (total === 0) return null
  const used = entries.reduce(
    (sum, d) =>
      sum + (d.partitions?.reduce((s, p) => s + (p.used_space || 0), 0) || 0),
    0,
  )
  return {
    totalGB: total / GB,
    usedGB: used > 0 ? used / GB : null,
  }
}

export const NodeCard = ({
  id,
  nodeInfo,
  selected,
  onClick,
  isMultiselectMode = false,
  isSelected = false,
  onSelect,
  dragEnabled = false,
  dragIds,
  inGroupDrag = false,
  onGroupDragChange,
  columns = nodeColumnsOrdered(),
  ...rest
}: NodeCardProps) => {
  const { t } = useTranslation()
  const locator = createLocator(rest)
  const [isDragging, setIsDragging] = useState(false)
  const { ous } = useOUs()
  const metricsHistory = useNodeMetrics(id)
  const favoriteIds = useFavoriteNodeIds()
  const isFavorite = favoriteIds.has(id)
  const buttonPressedNodes = useUnit($buttonPressedNodes)
  const buttonPulse = id in buttonPressedNodes
  const preferHostnames = usePreferHostnames()
  const primaryLabel = nodeDisplayName(nodeInfo, preferHostnames)

  const handleToggleFavorite = useCallback(
    (e: React.MouseEvent | React.KeyboardEvent) => {
      e.stopPropagation()
      toggleFavoriteFx({ nodeId: id, favorite: !isFavorite })
    },
    [id, isFavorite],
  )

  // The ID button — always visible so the affordance is discoverable while
  // scanning a rack (the panel's identify only exists after the right node is
  // already open). It LIGHTS UP while the device's locate LED blinks: for the
  // identify duration after a dispatch (from here or the panel), and for as
  // long as a physical press keeps this node marked.
  const check = useCan()
  const [identifyBusy, setIdentifyBusy] = useState(false)
  const identifyBlinkNodes = useUnit($identifyBlinkNodes)
  // Two ways to be lit, and they read differently on purpose: WE asked the
  // device to blink, versus somebody at the rack pressed its button — the
  // second renders as physically pressed in, because that is what happened.
  const identifying = id in identifyBlinkNodes
  const lit = identifying || buttonPulse
  const showIdButton =
    nodeInfo.approval_status === 'approved' &&
    // A watcher ("Watcher") node executes no commands at all — the platform
    // refuses identify with 409. A button that can only fail is worse than no
    // button, so the slot stays empty (and keeps its width).
    nodeInfo.agent_kind !== 'watcher' &&
    check(Actions.NodeStatusCheck, nodeInfo.ou_id)
  const canIdentify =
    showIdButton && nodeInfo.status === 'online' && !identifyBusy
  const handleIdentify = useCallback(
    async (e: React.MouseEvent | React.KeyboardEvent) => {
      e.stopPropagation()
      setIdentifyBusy(true)
      try {
        if (lit) {
          // Off. A real stop goes to the device only when a BLINK is showing;
          // a row lit by the physical-press MARK alone has nothing running on
          // the device, and the old always-dispatch made a dark LED flash for
          // five seconds on every mark clear — the rack-side confusion this
          // branch was rewritten for. (An agent older than the stop field
          // renders the stop as the legacy 5-second minimum blink.)
          if (identifying) {
            await identifyNode(id, { stop: true })
          }
          identifyBlinkStopped(id)
          buttonMarkCleared(id)
          showSuccess(
            identifying
              ? t('entities.nodeList.nodeCard.identifyStopped')
              : t('entities.nodeList.nodeCard.buttonMarkCleared'),
          )
          return
        }
        const { duration_sec } = await identifyNode(id)
        // The server answers with the account's effective duration; the web
        // lamp must run exactly as long as the device, never a constant.
        identifyBlinkStarted({ nodeId: id, durationMs: duration_sec * 1000 })
        showSuccess(
          t('entities.nodeList.nodeCard.blinking', {
            duration: formatBlink(duration_sec),
          }),
        )
      } catch (err) {
        showError(
          err instanceof Error && 'response' in err
            ? ((err as { response?: { data?: { message?: string } } }).response
                ?.data?.message ??
                t('entities.nodeList.nodeCard.identifyFailed'))
            : t('entities.nodeList.nodeCard.identifyFailed'),
        )
      } finally {
        setIdentifyBusy(false)
      }
    },
    // `identifying` decides whether the off-click sends a real stop to the
    // device; omitting it left a stale false in the closure and the stop
    // click never reached the device.
    [id, lit, identifying, t],
  )

  const status = nodeInfo.status || 'offline'
  const uptime = useUptime({
    bootedAt: nodeInfo.system_info?.hardware?.boot?.booted_at,
    lastSeenAt: nodeInfo.last_seen_at,
    status,
  })
  const normalizedStatus = normalizeStatus(status)
  const isOffline = ['offline', 'unreachable', 'shutdown'].includes(
    normalizedStatus,
  )
  // Mid-reboot the counter would tick on from the stale pre-reboot booted_at,
  // so it shows a dash until the agent reconnects with a fresh one. That rule
  // now lives inside useUptime, where it also covers the panel and every other
  // transitional state — this card correcting the value on its way out once
  // hid in the list what the sidebar showed plainly.
  // Cyberpunk status treatments: live nodes glow neon green, netbooted
  // (recovery) nodes glow violet, nodes waiting for approval/adoption pulse
  // amber.
  const displayState = nodeDisplayState(nodeInfo)
  const isRecovery = displayState === 'recovery'
  // Offline with the awaiting-return stamp is a different fact from plain
  // offline: the platform TOLD it to come back (install_os settles into a
  // self-reboot; the sweeper records what it was doing) and it never did.
  const neverReturned =
    nodeInfo.status === 'offline' && !!nodeInfo.awaiting_return_after
  // Pinned for work nobody asked to reboot: say "working" instead of
  // "restarting" — the same distinction the Status page already makes.
  const heldForWork = isNodeHeldForWork(
    nodeInfo.status,
    nodeInfo.pinned_by_slug,
  )
  const statusLabel =
    displayState === 'recovery'
      ? t('entities.nodeList.nodeCard.status.recovery')
      : displayState === 'pending' &&
          nodeInfo.approval_status === 'pending_approval'
        ? t('entities.nodeList.nodeCard.status.awaitingApproval')
        : neverReturned
          ? t('entities.nodeList.nodeCard.status.neverReturned')
          : heldForWork
            ? t('entities.nodeList.nodeCard.status.working')
            : nodeStatusText(nodeInfo.status || 'unreachable', null)
  const neverReturnedTitle = neverReturned
    ? t('entities.nodeList.nodeCard.neverReturnedTitle', {
        when: nodeInfo.awaiting_return_after,
      })
    : undefined

  // Power facts, null when the node has never reported any (non-Pi, or an agent
  // below 0.5.4) — absence must never render as healthy.
  const powerPill = nodePowerLine(nodeInfo)

  const ouName = nodeInfo.ou_id
    ? (ous.find((o) => o.id === nodeInfo.ou_id)?.name ?? '—')
    : '—'
  const osText =
    nodeInfo.system_info?.os?.details ||
    nodeInfo.system_info?.os?.version ||
    '—'
  const storage = storageSummary(nodeInfo)

  const cellContext = {
    node: nodeInfo,
    ouName,
    osText,
    uptime,
    isOffline,
    storage,
    metrics: isOffline ? undefined : metricsHistory[metricsHistory.length - 1],
    isFavorite,
    onToggleFavorite: handleToggleFavorite,
  }

  const handleClick = useCallback(() => {
    if (isMultiselectMode) {
      onSelect?.(id)
    } else {
      onClick?.(id)
    }
  }, [onClick, onSelect, id, isMultiselectMode])

  // Native HTML5 drag: carry the node id(s) so OU tree rows can assign on drop.
  // A comma-separated list lets a multiselect drag move the whole selection.
  const handleDragStart = useCallback(
    (e: React.DragEvent<HTMLDivElement>) => {
      const ids = dragIds && dragIds.length > 0 ? dragIds : [id]
      e.dataTransfer.setData(NODE_DND_TYPE, ids.join(','))
      e.dataTransfer.effectAllowed = 'move'
      // When dragging a group, show a count badge as the drag image so it is
      // obvious the whole selection moves.
      if (ids.length > 1) {
        const ghost = document.createElement('div')
        ghost.textContent = t('entities.nodeList.nodeCard.dragGhost', {
          count: ids.length,
        })
        ghost.style.cssText =
          'position:absolute;top:-1000px;left:-1000px;padding:6px 10px;' +
          'background:#171717;color:#a3e635;border:1px solid #a3e635;' +
          'border-radius:6px;font:600 12px ui-monospace,monospace;white-space:nowrap;'
        document.body.appendChild(ghost)
        e.dataTransfer.setDragImage(ghost, 0, 0)
        setTimeout(() => document.body.removeChild(ghost), 0)
      }
      setIsDragging(true)
      // Tell the list how many rows this drag carries, so every OTHER row in
      // the selection can show it is moving too. The drag image alone was not
      // enough: it sits under the cursor, away from the rows, and an operator
      // watching the list saw one row dim while twelve were reassigned.
      onGroupDragChange?.(ids.length)
    },
    [id, dragIds, onGroupDragChange, t],
  )

  const handleDragEnd = useCallback(() => {
    setIsDragging(false)
    onGroupDragChange?.(0)
  }, [onGroupDragChange])

  const handleCheckboxChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      e.stopPropagation()
      onSelect?.(id)
    },
    [onSelect, id],
  )

  return (
    <div
      draggable={dragEnabled}
      onDragStart={handleDragStart}
      onDragEnd={handleDragEnd}
      className={clsx(
        'flex items-center gap-4 pl-0 pr-4',
        'select-none rounded transition-colors',
        // Someone pressed the PHYSICAL button on this machine — the
        // device-to-web half of identify. The blinking is the status tube's
        // job now; the row only has to be findable while scrolling, so it
        // takes a steady outline in the same blue rather than a second
        // animation competing with the tube and the button.
        buttonPulse && 'outline outline-1 outline-blue-6/70 bg-blue-9/10',
        dragEnabled && 'cursor-grab active:cursor-grabbing',
        // The row under the cursor and every other row travelling with it read
        // the same, because they are doing the same thing.
        (isDragging || inGroupDrag) && 'opacity-50',
        inGroupDrag && !isDragging && 'outline outline-1 outline-lime-6/40',
        isRecovery
          ? selected || (isMultiselectMode && isSelected)
            ? 'bg-violet-6/20'
            : 'bg-violet-6/[0.08] hover:bg-violet-6/[0.14]'
          : selected && !isMultiselectMode
            ? 'bg-neutral-12'
            : 'bg-neutral-14 hover:bg-neutral-13',
        !isRecovery && isMultiselectMode && isSelected && 'bg-neutral-12',
      )}
      {...locator()}
    >
      <button
        type="button"
        className="flex items-center gap-4 flex-1 min-w-0 text-left py-3"
        onClick={handleClick}
      >
        <StatusTube state={displayState} label={statusLabel} blinking={lit} />
        {isMultiselectMode && (
          <div
            className="flex-shrink-0 flex items-center gap-1.5"
            onClick={(e) => e.stopPropagation()}
          >
            <input
              type="checkbox"
              checked={isSelected}
              onChange={handleCheckboxChange}
              onClick={(e) => e.stopPropagation()}
              className="w-4 h-4 accent-lime-6 bg-neutral-14 border-neutral-12 rounded focus:ring-lime-6 focus:ring-2 cursor-pointer"
            />
            {/* Grab handle — signals the row (and, when selected, the whole
                selection) can be dragged onto a unit. */}
            <svg
              width="10"
              height="14"
              viewBox="0 0 16 16"
              fill="currentColor"
              aria-hidden="true"
              className={clsx(
                'flex-shrink-0',
                isSelected ? 'text-neutral-5' : 'text-neutral-8',
              )}
            >
              <circle cx="5" cy="4" r="1.3" />
              <circle cx="11" cy="4" r="1.3" />
              <circle cx="5" cy="8" r="1.3" />
              <circle cx="11" cy="8" r="1.3" />
              <circle cx="5" cy="12" r="1.3" />
              <circle cx="11" cy="12" r="1.3" />
            </svg>
          </div>
        )}

        {/* Identity: name + status label + pills. Takes the width the metric
            columns leave, from 14rem down to 11rem (the multiselect checkbox
            needs the difference); below the header breakpoint it is the only
            flexible part, so the compact IP stays untruncated. */}
        <div className="flex flex-col min-w-0 flex-1 @[30rem]:basis-56 @[30rem]:min-w-44">
          <div className="flex items-center gap-2 min-w-0">
            <span
              className="text-sm font-medium text-neutral-1 truncate"
              {...locator.name({ name: nodeInfo.name || nodeInfo.hostname })}
            >
              {primaryLabel ||
                t('entities.nodeList.nodeCard.fallbackName', {
                  serial:
                    nodeInfo.processor_serial?.slice(-8) ||
                    t('entities.nodeList.nodeCard.unknownSerial'),
                })}
            </span>
            {nodeInfo.approval_status === 'pending_approval' && (
              <span className="text-xxs bg-yellow-6 text-neutral-14 px-2 py-0.5 rounded whitespace-nowrap">
                {t('entities.nodeList.nodeCard.badgePending')}
              </span>
            )}
            {nodeInfo.approval_status === 'rejected' && (
              <span className="text-xxs bg-red-6 text-neutral-14 px-2 py-0.5 rounded whitespace-nowrap">
                {t('entities.nodeList.nodeCard.badgeRejected')}
              </span>
            )}
            {/* Quieter than the alarm pills beside it (rejected, watchdog off,
                under-voltage): a flashed bootloader waiting for its reboot is
                a pending step, not a fault, and a solid amber block at the
                pills' full size read as one. Tinted, bordered, half padding. */}
            {nodeInfo.bootloader_reboot_pending && (
              <span
                className="text-[9px] leading-4 tracking-wide bg-amber-6/15 text-amber-6 border border-amber-6/30 px-1.5 rounded-sm whitespace-nowrap"
                title={t('entities.nodeList.nodeCard.rebootPendingTitle')}
              >
                {t('entities.nodeList.nodeCard.badgeRebootPending')}
              </span>
            )}
            {nodeInfo.boot_failsafe_at != null && (
              <span
                className="text-xxs bg-amber-6 text-neutral-14 px-2 py-0.5 rounded whitespace-nowrap"
                title={t('entities.nodeList.nodeCard.autoRecoveredTitle')}
              >
                {t('entities.nodeList.nodeCard.badgeAutoRecovered')}
              </span>
            )}
            {nodeInfo.watchdog_disarmed && (
              <span
                className="text-xxs bg-red-6 text-neutral-14 px-2 py-0.5 rounded whitespace-nowrap"
                title={t('entities.nodeList.nodeCard.watchdogDisarmedTitle', {
                  reason: nodeInfo.watchdog_disarm_reason
                    ? `: ${nodeInfo.watchdog_disarm_reason}`
                    : '',
                })}
              >
                {t('entities.nodeList.nodeCard.badgeWatchdogOff')}
              </span>
            )}
            {/* Under-voltage only — never thermal throttling. This pill has to
                stay worth reading: a board that gets warm under load is normal,
                while a supply that cannot feed it corrupts installs and captures
                silently and makes disks stop answering. Shown for the sticky bit
                too, because that is what explains a failure after the fact. */}
            {powerPill?.supplyProblem && (
              <span
                className="text-xxs bg-red-6 text-neutral-14 px-2 py-0.5 rounded whitespace-nowrap"
                title={powerPill.title}
              >
                {t('entities.nodeList.nodeCard.badgeUnderVoltage')}
              </span>
            )}
          </div>
          <span
            className={clsx(
              'text-xxs uppercase tracking-wide',
              displayState === 'online' &&
                'text-green-6 [text-shadow:0_0_10px_rgba(64,255,61,0.55)]',
              displayState === 'recovery' &&
                'text-violet-5 [text-shadow:0_0_10px_rgba(142,102,255,0.65)]',
              displayState === 'pending' &&
                'text-yellow-6 [text-shadow:0_0_10px_rgba(255,247,61,0.45)]',
              (displayState === 'offline' || displayState === 'other') &&
                !heldForWork &&
                'text-neutral-7',
              // Work in progress reads as its own thing, not as a dead node.
              heldForWork && 'text-blue-6',
            )}
            title={
              heldForWork
                ? t('entities.nodeList.nodeCard.status.workingTitle', {
                    command: nodeInfo.pinned_by_slug ?? '',
                  })
                : neverReturnedTitle
            }
          >
            {statusLabel}
          </span>
        </div>

        {/* ID — between the identity column and the metric columns, centred on
            the row rather than sitting on the name line, so the buttons line
            up down the list whatever each name's length. The slot keeps its
            width when the button is not offered, or every column to its right
            would shift row by row. */}
        <div className="flex-shrink-0 w-8 flex items-center justify-center">
          {showIdButton && (
            /* span, not button: the whole row is already a button. */
            <span
              role="button"
              tabIndex={canIdentify ? 0 : -1}
              aria-pressed={lit}
              onClick={
                canIdentify ? handleIdentify : (e) => e.stopPropagation()
              }
              onKeyDown={(e) => {
                if (canIdentify && (e.key === 'Enter' || e.key === ' ')) {
                  e.preventDefault()
                  handleIdentify(e)
                }
              }}
              aria-label={
                lit
                  ? t('entities.nodeList.nodeCard.identifyStopLabel')
                  : t('entities.nodeList.nodeCard.identifyLabel')
              }
              title={
                buttonPulse
                  ? t('entities.nodeList.nodeCard.buttonPulseTitle')
                  : identifying
                    ? t('entities.nodeList.nodeCard.identifyingTitle')
                    : canIdentify
                      ? t('entities.nodeList.nodeCard.identifyTitle')
                      : t('entities.nodeList.nodeCard.identifyOfflineTitle')
              }
              className={clsx(
                'flex items-center justify-center w-7 h-7 rounded border',
                'text-[10px] font-semibold tracking-wide transition-colors',
                // One shadow utility per branch: two arbitrary shadow classes
                // on one element both write --tw-shadow, and which one wins is
                // decided by the generated CSS order, not by this list.
                buttonPulse
                  ? // Pressed on the DEVICE reads as pressed here.
                    'border-blue-4 bg-blue-5 text-neutral-14 translate-y-px animate-pulse shadow-[inset_0_2px_5px_rgba(0,0,0,0.45),0_0_12px_rgba(107,166,255,0.95)]'
                  : identifying
                    ? 'border-blue-4 bg-blue-5 text-neutral-14 animate-pulse shadow-[0_0_12px_rgba(107,166,255,0.95)]'
                    : canIdentify
                      ? 'border-blue-8 bg-blue-9/30 text-blue-4 hover:bg-blue-8/50 hover:text-blue-2 cursor-pointer'
                      : // Offline: dim, but still the same control — a neutral
                        // grey box reads as a different thing entirely.
                        'border-blue-9 bg-blue-10/30 text-blue-8 cursor-default',
              )}
            >
              {t('entities.nodeList.nodeCard.idButton')}
            </span>
          )}
        </div>

        {/* Metric columns — labels live in the shared NodeList header */}
        <div className="flex items-center gap-6 flex-none">
          {columns.map((column) => (
            <div
              key={column.key}
              className={clsx(column.className, 'text-sm text-neutral-3')}
            >
              {column.render(cellContext)}
            </div>
          ))}
        </div>

        {/* Compact address, shown only while the Address column is hidden */}
        <div
          className="@[36rem]:hidden flex-none text-sm text-neutral-4 font-mono"
          title={nodeInfo.ipv4 || nodeInfo.processor_serial || undefined}
          {...locator.ip({ ip: nodeInfo.ipv4 })}
        >
          {nodeInfo.ipv4 || nodeInfo.processor_serial || '—'}
        </div>

        {/* Gutter matching the header's column-visibility control */}
        <div className="hidden @[30rem]:block w-6 flex-shrink-0" aria-hidden />
      </button>
    </div>
  )
}
