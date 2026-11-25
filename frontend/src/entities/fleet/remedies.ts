import { reapplyNodeAccess } from '@/entities/access/api'
import { applyDeviceProfile } from '@/entities/deviceProfile/api'
import { deviceCapReasonText } from '@/entities/entitlement/deviceCapText'
import { apiNodes } from '@/entities/node/api'
import { skipReasonText } from '@/entities/node/skipReason'
import { Actions } from '@/entities/permissions/actions'
import { applyNodeUtilities } from '@/entities/utility/api'

import { extractApiErrorMessage } from '@/shared/utils/apiError'

import type { AttentionItem, AttentionKind } from './api'

// What an operator can DO about a condition, straight from the Status page.
//
// Every remedy dispatches an endpoint that already exists and already gates
// itself. That is the point: the page adds no permission, no second
// dispatch path and no second audit story — it is the buttons from the node
// sidebar, reachable for many nodes at once. A condition with no honest
// one-click answer (offline, a unit that no longer exists, a watcher that
// cannot netboot) deliberately has no entry here; inventing one would be a
// button that pretends.

/** How one node's remedy turned out. Four states, because three of the
 *  endpoints answer "accepted but not applied" and calling that success is how
 *  a queued command reads as a finished one. */
export type RemedyOutcome = {
  nodeId: string
  nodeName: string
  state: 'dispatched' | 'queued' | 'skipped' | 'failed'
  /** The platform's own words for a skip or a failure. Shown verbatim. */
  reason?: string
}

export type RemedyTarget = { nodeId: string; nodeName: string }

export type Remedy = {
  kind: AttentionKind
  /** The permission the endpoint behind this checks, per node OU. */
  action: string
  /** i18n suffix under pages.status.remedies.<id>. */
  id: string
  /**
   * The command this dispatches, matching the slug the server reports a failed
   * attempt under. Absent where the remedy sends the device nothing — clearing
   * a failsafe and approving a node are platform writes with no command to
   * fail.
   */
  slug?: string
  /**
   * The device reboots as part of this. Stated inline in the confirmation,
   * never behind a hover: a warning hidden behind hover is a warning unread.
   */
  reboots?: boolean
  /**
   * The command behind this is in the platform's managed-OS-only set, so it is
   * HELD while the node runs the recovery image — registered, never delivered,
   * until the node boots its installed OS. Offering it there is a button that
   * appears to do nothing, which is how this flag came to exist.
   */
  managedOSOnly?: boolean
  /**
   * Not held but REFUSED on a netbooted node: the ROM applies recovery.bin only
   * from SD or USB, so a device with no card cannot reflash its own EEPROM. The
   * agent answers with exactly that and the command fails in under a tenth of a
   * second. The node panel has the media-aware gate; this list does not carry
   * the storage inventory, so it withholds rather than guesses.
   */
  refusedInRecovery?: boolean
  /**
   * Demands an explicit acknowledgement before the action is enabled, not just
   * a Continue. For writing firmware and for rebooting — the two things here
   * whose cost an operator cannot take back with another click.
   */
  acknowledge?: boolean
  /**
   * The command behind this is NOT queueable, so the platform refuses it
   * outright unless the node is online or in maintenance: "node is not
   * available (current status: …)". A reboot offered to a node that is already
   * restarting is the typical case — the operator presses it and gets
   * an error, because the node is in the middle of doing exactly that.
   *
   * Queueable remedies deliberately do NOT carry this: queuing an agent update
   * for a node that is away is the feature, not a mistake.
   */
  requiresConnected?: boolean
  /**
   * A remedy that is a PLACE rather than an action: the condition is fixed
   * somewhere else (the device cap, on the Subscription tab), so the row
   * links there and there is nothing to dispatch, select or confirm.
   */
  opens?: {
    to: '/account'
    search: { tab: 'subscription'; sidebar: undefined }
  }
  /** Absent exactly when `opens` is set. */
  run?: (targets: RemedyTarget[]) => Promise<RemedyOutcome[]>
}

/** Runs a per-node endpoint over the selection, a few at a time.
 *
 * Bounded rather than Promise.all over everything: a fleet-wide selection is
 * up to the per-account node cap, and firing that many EEPROM bakes or utility
 * applies at once is a self-inflicted load spike on the operator's own server.
 */
const perNode = async (
  targets: RemedyTarget[],
  call: (nodeId: string) => Promise<RemedyOutcome>,
  concurrency = 4,
): Promise<RemedyOutcome[]> => {
  const out: RemedyOutcome[] = new Array(targets.length)
  let next = 0
  const worker = async () => {
    for (;;) {
      const i = next++
      if (i >= targets.length) return
      const target = targets[i]
      try {
        out[i] = await call(target.nodeId)
      } catch (error) {
        out[i] = {
          nodeId: target.nodeId,
          nodeName: target.nodeName,
          state: 'failed',
          // Empty rather than a canned English sentence: the wording for
          // "failed, no reason given" belongs to the dialog, which can
          // translate it. A device-limit refusal (an approval) is worded from
          // its code, so a limit that could not be checked never reads as
          // the limit reached.
          reason:
            deviceCapReasonText(error) ??
            (extractApiErrorMessage(error, '') || undefined),
        }
      }
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(concurrency, targets.length) }, worker),
  )
  return out
}

// Two conditions end in the same act — firmware and hardware settings both
// come up on the next boot — so they share one implementation rather than two
// copies of the same call.
const rebootAll = (targets: RemedyTarget[]): Promise<RemedyOutcome[]> =>
  perNode(targets, async (nodeId) => {
    await apiNodes.rebootNode(nodeId)
    return {
      nodeId,
      nodeName: targets.find((t) => t.nodeId === nodeId)?.nodeName ?? nodeId,
      state: 'dispatched' as const,
    }
  })

export const REMEDIES: Remedy[] = [
  {
    kind: 'agent_outdated',
    slug: 'update_agent',
    id: 'updateAgent',
    action: Actions.NodeUpdateAgent,
    managedOSOnly: true,
    // The one remedy with a real batch endpoint: update_agent is a mapped
    // command slug, so the whole selection goes in one request and the
    // platform authorizes each node's unit itself.
    run: async (targets) => {
      const byId = new Map(targets.map((t) => [t.nodeId, t.nodeName]))
      const result = await apiNodes.postBatchNodeCommand({
        node_ids: targets.map((t) => t.nodeId),
        command: 'update_agent',
        // "latest" is a literal filename on the CDN, and it is what the node
        // sidebar's one-click update sends too.
        params: { agent_version: 'latest', allow_downgrade: false },
      })
      return result.results.map((item) => ({
        nodeId: item.node_id,
        nodeName: byId.get(item.node_id) ?? item.node_id,
        state: item.skipped
          ? ('skipped' as const)
          : !item.success
            ? ('failed' as const)
            : item.queued
              ? ('queued' as const)
              : ('dispatched' as const),
        reason:
          skipReasonText(item.skip_reason) ||
          item.error ||
          item.skip_reason ||
          undefined,
      }))
    },
  },
  {
    kind: 'bootloader_outdated',
    slug: 'update_boot_order',
    id: 'updateBootloader',
    action: Actions.NodeEEPROMFlash,
    // It does NOT reboot, and saying it did was wrong. The agent's own answer
    // is "Signed EEPROM image flashed successfully. Reboot required for the
    // new boot order to take effect" — the platform pins the node out of
    // service for the flash and releases it, and the firmware waits for a
    // reboot nobody has performed yet. Writing EEPROM is still the operation
    // that can cost a device its way back, so it takes an acknowledgement.
    acknowledge: true,
    refusedInRecovery: true,
    requiresConnected: true,
    run: (targets) =>
      perNode(targets, async (nodeId) => {
        // No boot order: the per-serial bake keeps the device's own, which is
        // what both sidebar callers send.
        await apiNodes.provisionEEPROM(nodeId)
        return {
          nodeId,
          nodeName:
            targets.find((t) => t.nodeId === nodeId)?.nodeName ?? nodeId,
          state: 'dispatched' as const,
        }
      }),
  },
  {
    kind: 'device_profile_pending',
    slug: 'apply_device_profile',
    id: 'applyDeviceProfile',
    action: Actions.NodeFileWrite,
    managedOSOnly: true,
    run: (targets) =>
      perNode(targets, async (nodeId) => {
        const res = await applyDeviceProfile(nodeId)
        return {
          nodeId,
          nodeName:
            targets.find((t) => t.nodeId === nodeId)?.nodeName ?? nodeId,
          state: res.queued ? ('queued' as const) : ('dispatched' as const),
        }
      }),
  },
  {
    kind: 'utility_drift',
    slug: 'apply_utility_config',
    id: 'applyUtilities',
    action: Actions.UtilityManage,
    managedOSOnly: true,
    run: (targets) =>
      perNode(targets, async (nodeId) => {
        const res = await applyNodeUtilities(nodeId)
        return {
          nodeId,
          nodeName: res.node_name || nodeId,
          state: res.skipped
            ? ('skipped' as const)
            : res.queued
              ? ('queued' as const)
              : ('dispatched' as const),
          reason: res.reason || undefined,
        }
      }),
  },
  {
    kind: 'device_profile_reboot_pending',
    slug: 'reboot',
    id: 'rebootForProfile',
    action: Actions.NodeReboot,
    reboots: true,
    acknowledge: true,
    requiresConnected: true,
    run: (targets) => rebootAll(targets),
  },
  {
    kind: 'bootloader_reboot_pending',
    slug: 'reboot',
    id: 'rebootForBootloader',
    action: Actions.NodeReboot,
    // The other half of a bootloader update, and the half the platform will
    // never do by itself. This one really does take the device down.
    reboots: true,
    acknowledge: true,
    requiresConnected: true,
    run: (targets) => rebootAll(targets),
  },
  {
    kind: 'access_apply_failed',
    id: 'reapplyAccess',
    slug: 'apply_access_config',
    action: Actions.AccessManage,
    // Held in recovery: the initramfs has no groupadd, no usermod and no
    // persistent root, so the apply is queued until the node boots its OS.
    managedOSOnly: true,
    run: (targets) =>
      perNode(targets, async (nodeId) => {
        const res = await reapplyNodeAccess(nodeId)
        return {
          nodeId,
          nodeName:
            targets.find((t) => t.nodeId === nodeId)?.nodeName ?? nodeId,
          state: res.queued ? ('queued' as const) : ('dispatched' as const),
        }
      }),
  },
  {
    kind: 'boot_failsafe',
    id: 'clearBootFailsafe',
    action: Actions.NodeUpdate,
    // Platform state only: it clears the flag and the withheld-attempt
    // counter, sends the device nothing, and says nothing about whether the
    // node can boot its OS now.
    run: (targets) =>
      perNode(targets, async (nodeId) => {
        await apiNodes.postClearBootFailsafe(nodeId)
        return {
          nodeId,
          nodeName:
            targets.find((t) => t.nodeId === nodeId)?.nodeName ?? nodeId,
          state: 'dispatched' as const,
        }
      }),
  },
  {
    kind: 'pending_approval',
    id: 'approve',
    action: Actions.NodeApprove,
    // Approving can write the network-first EEPROM where the unit's policy
    // asks for it. That write does not reboot either — the firmware takes
    // effect on the node's next boot.
    run: (targets) =>
      perNode(targets, async (nodeId) => {
        await apiNodes.approveNode(nodeId)
        return {
          nodeId,
          nodeName:
            targets.find((t) => t.nodeId === nodeId)?.nodeName ?? nodeId,
          state: 'dispatched' as const,
        }
      }),
  },
  {
    kind: 'device_cap_reached',
    id: 'openSubscription',
    // The cap is lifted by removing a device or applying an entitlement,
    // neither of which is a per-node action: the row links to the place
    // where the entitlement is shown and applied.
    action: Actions.EntitlementManage,
    opens: {
      to: '/account',
      search: { tab: 'subscription', sidebar: undefined },
    },
  },
]

/** The remedies that dispatch something, the only ones a selection can run. */
export const DISPATCH_REMEDIES = (): Remedy[] => REMEDIES.filter((r) => !!r.run)

export const remedyFor = (kind: AttentionKind): Remedy | undefined =>
  REMEDIES.find((r) => r.kind === kind)

/**
 * The remedy for one item as the server reported it. The device-limit row
 * links to the Subscription tab only for the account that operates the
 * install: anyone else has nothing to apply there. An older backend sends no
 * flag and keeps the link, as before.
 */
export const remedyForItem = (
  item: Pick<AttentionItem, 'kind' | 'instance_operator'>,
): Remedy | undefined => {
  if (item.kind === 'device_cap_reached' && item.instance_operator === false) {
    return undefined
  }
  return remedyFor(item.kind)
}
