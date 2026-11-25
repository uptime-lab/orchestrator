import { t } from 'i18next'
import { colorSystem } from '@root/tailwind.config.ts'

import { NodeData, NodeRole, NodeStatus } from '@/entities/node/api/types.ts'
import { boardLabel, type BoardLabel } from '@/entities/node/board.ts'

import { exhaustiveMatchingGuard } from '@/shared/utils/exhaustiveSwitchCase.ts'

/**
 * Normalizes status string to ensure it matches NodeStatus type
 */
export const normalizeStatus = (status: string): NodeStatus => {
  const normalized = status.toLowerCase().trim()
  // Map valid statuses
  const validStatuses: NodeStatus[] = [
    'unreachable',
    'offline',
    'online',
    'maintenance',
    'standby',
    'ready_for_adoption',
    'flashing',
    'adopting',
    'adoption_failed',
    'restarting',
    'shutting_down',
    'shutdown',
    'busy',
    'os_installing',
    'os_installation_failed',
  ]
  if (validStatuses.includes(normalized as NodeStatus)) {
    return normalized as NodeStatus
  }
  // Default to unreachable if status is unknown
  return 'unreachable'
}

export const NodeStatusColourClassMap: Record<NodeStatus, string[]> = {
  standby: ['text-yellow-6'],
  online: ['text-green-6'],
  offline: ['text-red-6'],
  // Maintenance = executing commands; violet is reserved for netboot
  // recovery (nodeDisplayState/isNetbootRecovery), not raw maintenance.
  maintenance: ['text-orange-5'],
  unreachable: ['text-neutral-8'],
  ready_for_adoption: ['text-yellow-6'],
  // Flashing = EEPROM write in progress; blue reads as an active operation.
  flashing: ['text-blue-6'],
  adopting: ['text-neutral-8'],
  adoption_failed: ['text-red-6'],
  restarting: ['text-yellow-6'],
  shutting_down: ['text-yellow-6'],
  shutdown: ['text-yellow-6'],
  busy: ['text-blue-6'],
  os_installing: ['text-yellow-6'],
  os_installation_failed: ['text-red-6'],
}

export const NodeStatusColourMap: Record<NodeStatus, keyof typeof colorSystem> =
  {
    standby: 'yellow',
    online: 'green',
    offline: 'red',
    maintenance: 'orange',
    unreachable: 'neutral',
    ready_for_adoption: 'yellow',
    flashing: 'blue',
    adopting: 'neutral',
    adoption_failed: 'red',
    restarting: 'yellow',
    shutting_down: 'yellow',
    shutdown: 'yellow',
    busy: 'blue',
    os_installing: 'yellow',
    os_installation_failed: 'red',
  }

// A node "in recovery" is one running the network bootstrap — it reports
// being booted over HTTP. Maintenance alone is NOT recovery: a normal
// disk-booted node enters maintenance while executing commands (agent
// update, reboot) and must keep its regular status badge.
export const isNetbootRecovery = (node: NodeData): boolean => {
  const normalized = normalizeStatus(node.status || '')
  return (
    (normalized === 'online' || normalized === 'maintenance') &&
    node.system_info?.hardware?.boot?.current_boot_device === 'http'
  )
}

// isNodeInConnectedState mirrors the backend's node.IsInConnectedState — the
// rule that decides whether DELETE /nodes/:id refuses without ?force=true and
// whether a forced delete dispatches release_identity. It is deliberately NOT
// "online": a node wedged part-way through an install sits in maintenance or
// restarting with a live stream, and that is the most common delete there is.
// Encoding "online" here left the panel unable to delete exactly those nodes —
// the 409 named a state the panel was not sending force for, and no control
// there could set the flag.
export const isNodeInConnectedState = (status: string | undefined): boolean => {
  const normalized = normalizeStatus(status || '')
  return (
    normalized === 'online' ||
    normalized === 'maintenance' ||
    normalized === 'restarting'
  )
}

// REBOOT_SLUG is the one command that really takes the device down. The
// platform pins a node to "restarting" at DISPATCH for every command that MIGHT
// (a bootloader flash, an EEPROM enable), because it cannot trust an agent about
// a reboot it has not performed yet — and for a flash that claim is usually
// false. Calling that window "restarting" tells an operator the machine is going
// down when it is not, which is the complaint this pair answers.
const REBOOT_SLUG = 'reboot'

// isNodeHeldForWork reports a pin imposed by a command that was never asked to
// reboot. It reads nodes.pinned_by_slug, which the backend records at the same
// moment it sets the status, so every WebSocket push carries it.
//
// Restricted to "restarting" on purpose: "maintenance" is already a calm,
// accurate word for the same situation and needs no translation.
export const isNodeHeldForWork = (
  status: string | undefined,
  pinnedBySlug: string | undefined,
): boolean =>
  normalizeStatus(status || '') === 'restarting' &&
  !!pinnedBySlug &&
  pinnedBySlug !== REBOOT_SLUG

// isNodeDispatchable mirrors the backend's dispatch guard (services/node:
// a non-queueable command is accepted for "online" or "maintenance" and refused
// for anything else). The panel used to ask `status === 'online'` everywhere,
// which is stricter than the server for no reason: a node held by one command
// could not be sent the next step of the very flow the panel was walking the
// operator through.
export const isNodeDispatchable = (status: string | undefined): boolean => {
  const normalized = normalizeStatus(status || '')
  return normalized === 'online' || normalized === 'maintenance'
}

// The four visual groups the node list styles: live, recovery (netboot),
// waiting for approval/adoption, dead, and everything transitional.
export type NodeDisplayState =
  | 'online'
  | 'recovery'
  | 'pending'
  | 'offline'
  | 'other'

export const nodeDisplayState = (node: NodeData): NodeDisplayState => {
  if (isNetbootRecovery(node)) return 'recovery'
  const normalized = normalizeStatus(node.status || '')
  if (
    node.approval_status === 'pending_approval' ||
    normalized === 'ready_for_adoption' ||
    normalized === 'flashing' ||
    normalized === 'adopting'
  ) {
    return 'pending'
  }
  if (normalized === 'online') return 'online'
  if (['offline', 'unreachable', 'shutdown'].includes(normalized)) {
    return 'offline'
  }
  return 'other'
}

export const nodeStatusText = (
  status: NodeStatus | string,
  addressForOnlineDisplay: string | null,
): string => {
  const normalizedStatus = normalizeStatus(status)

  switch (normalizedStatus) {
    case 'online':
      return (
        addressForOnlineDisplay || t('entities.nodeList.nodeCard.status.online')
      )
    case 'offline':
      return t('entities.nodeList.nodeCard.status.offline')
    case 'maintenance':
      return t('entities.nodeList.nodeCard.status.maintenance')
    case 'standby':
      return t('entities.nodeList.nodeCard.status.ready')
    case 'unreachable':
      return t('entities.nodeList.nodeCard.status.unreachable')
    case 'ready_for_adoption':
      return t('entities.nodeList.nodeCard.status.readyForAdoption')
    case 'flashing':
      return t('entities.nodeList.nodeCard.status.flashing')
    case 'adopting':
      return t('entities.nodeList.nodeCard.status.adopting')
    case 'adoption_failed':
      return t('entities.nodeList.nodeCard.status.adoptionFailed')
    case 'restarting':
      return t('entities.nodeList.nodeCard.status.restarting')
    case 'shutting_down':
      return t('entities.nodeList.nodeCard.status.shuttingDown')
    case 'shutdown':
      return t('entities.nodeList.nodeCard.status.shutdown')
    case 'busy':
      return t('entities.nodeList.nodeCard.status.busy')
    case 'os_installing':
      return t('entities.nodeList.nodeCard.status.osInstalling')
    case 'os_installation_failed':
      return t('entities.nodeList.nodeCard.status.osInstallationFailed')
    default:
      exhaustiveMatchingGuard(normalizedStatus)
      return normalizedStatus // Fallback to status string if unknown
  }
}

export const nodeRoleText = (role: NodeRole) => {
  switch (role) {
    case NodeRole.Primary:
      return t('entities.nodeList.nodeCard.role.primary')
    case NodeRole.Secondary:
      return t('entities.nodeList.nodeCard.role.secondary')
    default:
      return exhaustiveMatchingGuard(role)
  }
}

export const sortNodes = (a: NodeData, b: NodeData) => (a.id > b.id ? 1 : -1)

// nodeDisplayName is the label the fleet list and the node panel show for a
// node, honouring the "prefer hostnames" display preference. Anything that
// ORDERS nodes by name must use the same rule: sorting by a name the column
// does not display reads as a broken sort.
export const nodeDisplayName = (
  node: Pick<NodeData, 'name' | 'hostname'>,
  preferHostnames: boolean,
): string =>
  (preferHostnames ? node.hostname || node.name : node.name || node.hostname) ||
  ''

const GiB = 1024 ** 3

// nodeBoard is the node's board badge — "Pi 5", "CM5 Lite", "CM4"… — or null
// for hardware this platform does not recognise. Shared with the node sidebar
// (see entities/node/board.ts) so the fleet list and the detail panel cannot
// disagree about the same device, which is what happened while each kept its
// own copy of the classifier.
export const nodeBoard = (node: NodeData): BoardLabel | null =>
  boardLabel(
    node.system_info?.hardware?.cpu?.model,
    node.system_info?.hardware?.cpu?.board_revision,
  )

// nodeRamGB returns installed RAM in whole GB (rounded), or null if unknown.
export const nodeRamGB = (node: NodeData): number | null => {
  const bytes = node.system_info?.hardware?.ram?.size
  if (!bytes || bytes <= 0) return null
  return Math.round(bytes / GiB)
}

// primaryDiskGB returns the capacity (GB) of the node's largest storage device,
// or null when no storage is reported.
//
// It used to prefer the largest NON-REMOVABLE device, which put the wrong number
// in the fleet list on exactly the nodes where it mattered: a node booting from
// a large USB disk showed the smaller SD card sitting beside it, because the USB
// medium reports removable=1. "Removable" is a fact about the slot, not about
// whether the machine runs on it — Raspberry Pis routinely boot from SD and from
// USB, and a node's ONLY medium can be a large SD card.
//
// Largest-wins is deliberately crude, and it is the honest crude: the column
// answers "how much disk does this node have", and the device it is running from
// is shown in the node panel, where there is room to mark it.
export const primaryDiskGB = (node: NodeData): number | null => {
  const devices =
    node.system_info?.storage_devices ?? node.storage_devices ?? {}
  const entries = Object.values(devices).filter(
    (d) => (d.total_capacity || 0) > 0,
  )
  if (entries.length === 0) return null
  const largest = entries.reduce((a, b) =>
    (b.total_capacity || 0) > (a.total_capacity || 0) ? b : a,
  )
  return Math.round((largest.total_capacity || 0) / GiB)
}

// bootOrderCanReturnToNetboot mirrors the backend's
// node.BootOrderCanReturnToNetboot, which gates installing the telemetry-only
// Watcher agent. Installing it is a one-way door: a Watcher node executes no
// commands, so the only route back to managing it is a recovery boot, and that
// happens only if the device's own EEPROM asks the platform for a boot image.
//
// Mirrored rather than asked of the server so the panel can explain the refusal
// BEFORE the operator commits to an irreversible action — the server check is
// still the one that binds.
// An ALLOWLIST of crossings, matching the Go rule: anything not named here (and
// not `http`) ends the walk. A denylist fails open on an entry nobody has heard
// of, and the agent's hex decoder silently drops digits it cannot name, so an
// unknown local-media mode arrives as a shorter list with the blocker deleted.
// Object.create(null) because a plain object literal answers truthy for
// 'constructor' and 'toString' off the prototype chain — a lookup miss that a
// Go map cannot have.
const BOOT_ORDER_PASS_THROUGH: Record<string, boolean> = Object.assign(
  Object.create(null),
  { network: true, rpiboot: true },
)

// A FUNCTION, not a const record: the labels come from i18next, which is not
// initialised when this module is first evaluated (same reasoning as
// nodeColumns.tsx).
const bootOrderLabels = (): Record<string, string> =>
  Object.assign(Object.create(null), {
    sd: t('entities.node.bootOrder.labels.sdCard'),
    sd_card_detect: t('entities.node.bootOrder.labels.sdCard'),
    nvme: t('entities.node.bootOrder.labels.nvme'),
    usb_msd: t('entities.node.bootOrder.labels.usb'),
    bcm_usb_msd: t('entities.node.bootOrder.labels.usb'),
    restart: t('entities.node.bootOrder.labels.restart'),
    stop: t('entities.node.bootOrder.labels.stop'),
  })

export const bootOrderCanReturnToNetboot = (
  order: string[] | undefined,
): { ok: boolean; reason: string } => {
  if (!order || order.length === 0) {
    return {
      ok: false,
      reason: t('entities.node.bootOrder.reasonNotReported'),
    }
  }
  for (const raw of order) {
    const entry = raw.trim().toLowerCase()
    if (entry === 'http') return { ok: true, reason: '' }
    if (BOOT_ORDER_PASS_THROUGH[entry]) continue
    const label =
      bootOrderLabels()[entry] ??
      t('entities.node.bootOrder.labels.unknown', { entry: raw })
    return {
      ok: false,
      reason: t('entities.node.bootOrder.reasonBlockedBefore', {
        label,
        order: order.join(' → '),
      }),
    }
  }
  return {
    ok: false,
    reason: t('entities.node.bootOrder.reasonNoHttp', {
      order: order.join(' → '),
    }),
  }
}

// nodeDidNetbootIntoRecovery mirrors the server's first Watcher-install
// condition. The question is HOW the node got here, so it is answered by the
// boot device and not by the recovery_bootstrap flag: the same bootstrap image
// runs from a PROVISIONING CARD and reports "sd" with the flag set, and a card
// a human inserted is no evidence that this device's EEPROM reaches this
// platform. Netbooting is: the ROM asked us for an image and we answered.
export const nodeDidNetbootIntoRecovery = (node?: {
  system_info?: {
    hardware?: { boot?: { current_boot_device?: string } }
  }
}): boolean => node?.system_info?.hardware?.boot?.current_boot_device === 'http'
