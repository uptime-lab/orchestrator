import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// The Status section's two account-wide reads: what needs an operator's
// attention right now, and what the platform has been doing to the fleet.
// Both are views over nodes, so both are gated on node.read server-side and
// narrowed to the caller's units. Plain api functions per the entities
// convention; useQuery wrappers live in hooks.ts.

// AttentionKind mirrors domain/node_attention. The server publishes the whole
// inventory on every response, so a kind added there needs no release here to
// appear in the filter — but a kind we have no words for would render as its
// own identifier, so `pages.status.attention.kinds.*` carries one label each.
export type AttentionKind = string

export type AttentionSeverity = 'critical' | 'warning'

export type AttentionItem = {
  kind: AttentionKind
  severity: AttentionSeverity
  /** The node's side of a version comparison (agent, bootloader). */
  current?: string
  /** The newest version available for that comparison. */
  expected?: string
  /** A command slug, a utility id, a disarm reason, a boot order. */
  subject?: string
  count?: number
  since?: string
  /**
   * device_cap_reached only: whether the caller's account operates the
   * install. Absent from an older backend, which is read as the operator.
   * A non-operator's item carries no install-wide count.
   */
  instance_operator?: boolean
  /**
   * device_cap_reached and account_device_limit_reached: the caller's own
   * admitted count. On account_device_limit_reached, expected is the
   * account's own limit.
   */
  account_devices?: number
}

/** What the platform is doing to a node right now. */
export type AttentionBusy = {
  slug: string
  /** The agent's own 0-100, where it reports one. */
  progress?: number
}

/** One remedy that did not work, and where to read why. */
export type AttentionFailure = {
  /** The command slug, so a remedy can claim its own failure. */
  slug: string
  command_id: string
  at: string
}

export type AttentionNode = {
  node_id: string
  node_name: string
  hostname?: string
  ou_id?: string
  status: string
  /**
   * Running the recovery image rather than its installed OS. Every apply in
   * the platform's managed-OS-only set is HELD there, so a button that
   * dispatches one produces a command that waits with no delivery attempt and
   * reads as a click that did nothing.
   */
  in_recovery?: boolean
  /**
   * The unsettled command the platform has out to this node. Stacking another
   * dispatch behind one that has the agent mid-restart leaves both sent and
   * never acknowledged — and naming it matters as much as withholding: "busy"
   * on its own is indistinguishable from stuck, and a package install runs for
   * minutes.
   */
  busy?: AttentionBusy
  /**
   * The most recent attempt at each remedy command that ended in failure. The
   * agent's message is not here — it is encrypted and redaction-gated — so the
   * row carries the command id and the reason is fetched for the one command
   * an operator opens.
   */
  failures?: AttentionFailure[]
  /** The loudest of this node's items. */
  severity: AttentionSeverity
  items: AttentionItem[]
}

export type AttentionList = {
  nodes: AttentionNode[]
  count: number
  /** Per-kind totals over the nodes this caller can see. */
  counts: Record<string, number>
  /** The whole inventory, in the order the filter should show it. */
  kinds: AttentionKind[]
  /**
   * Checks this run could not make — an unreachable EEPROM signer, a release
   * listing that would not load. The page says so rather than letting an
   * incomplete list read as a clean fleet.
   */
  omitted: AttentionKind[]
}

export const getFleetAttention = async (
  ouId?: string,
): Promise<AttentionList> => {
  const response = await apiClient.get<ApiResponse<AttentionList>>(
    '/api/v1/fleet/attention',
    { params: ouId ? { ou_id: ouId } : undefined },
  )
  return response.data.data
}

// The command lifecycle, in the order it runs. Three of the terminal states
// are deliberately not failures: superseded (a newer command for the same
// target retired it unsent), expired (it waited past its deadline for an
// offline node) and skipped (a precondition refused it on arrival).
export const ACTIVITY_STATUSES = [
  'registered',
  'sent',
  'in_progress',
  'success',
  'failed',
  'superseded',
  'expired',
  'skipped',
] as const

export type ActivityStatus = (typeof ACTIVITY_STATUSES)[number]

export type ActivityRow = {
  id: string
  node_id: string
  node_name?: string
  ou_id?: string
  slug: string
  status: ActivityStatus
  progress?: number
  /** A queued command is dispatched, not applied. */
  queueable?: boolean
  details?: string
  attempts?: number
  created_at: string
  updated_at: string
}

export type ActivityList = {
  activity: ActivityRow[]
  count: number
  total: number
  page: number
  per_page: number
}

export type ActivityParams = {
  ouId?: string
  nodeId?: string
  statuses?: string[]
  slug?: string
  since?: string
  page?: number
  perPage?: number
}

export const getFleetActivity = async (
  params: ActivityParams,
): Promise<ActivityList> => {
  const response = await apiClient.get<ApiResponse<ActivityList>>(
    '/api/v1/fleet/activity',
    {
      params: {
        ou_id: params.ouId,
        node_id: params.nodeId,
        // Comma-separated rather than repeated: axios serialises an array as
        // `status[]=`, which the handler does not read.
        status: params.statuses?.length ? params.statuses.join(',') : undefined,
        slug: params.slug || undefined,
        since: params.since,
        page: params.page,
        per_page: params.perPage,
      },
    },
  )
  return response.data.data
}

/**
 * The one command an operator opened. Served by the existing per-command
 * route, which redacts the details for a caller without secret.read_value and
 * records the read when it does not — the reason this list never carries the
 * text itself.
 */
export type CommandDetail = {
  id: string
  slug: string
  status: string
  details?: string
  /**
   * What the agent reported back. `details` is often a summary — a utility
   * apply says "failed utilities: tailscale" — while this carries the tool's
   * own words. Values are placeholder-redacted for a caller without
   * secret.read_value; the keys always survive.
   */
  result?: Record<string, string>
  created_at: string
  updated_at: string
}

export const getCommandDetail = async (
  commandId: string,
): Promise<CommandDetail> => {
  const response = await apiClient.get<ApiResponse<CommandDetail>>(
    `/api/v1/node-commands/${commandId}`,
  )
  return response.data.data
}
