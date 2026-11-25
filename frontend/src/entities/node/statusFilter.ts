import type { DeletedNode } from '@/entities/node/api/deletedNodes'
import type { NodeData } from '@/entities/node/api/types.ts'

import { isNetbootRecovery, normalizeStatus } from './utils.ts'

/**
 * The node list's status filter.
 *
 * A pure module, so the predicate is testable without the page. It decides what
 * each option MEANS — the page only applies it.
 *
 * The five non-`all` options partition the fleet: every node is in exactly one
 * of them, so `all` is their union and nothing can be reachable only through
 * `all`. That property is what makes "why can't I find this node" answerable
 * by clicking through the options one by one. Two consequences worth stating:
 *
 *  - `online` is "connected and in no special mode", which includes the
 *    transitional states (restarting, busy, installing) and the adoption
 *    states (awaiting approval, adopting). It used to be simply "not offline";
 *    the recovery and maintenance modes were carved out of it, so the label a
 *    row shows and the option that lists it now agree.
 *  - `recovery` wins over `maintenance`: a netbooted node executing a command
 *    reports `maintenance`, but its row says Recovery, and an operator who
 *    filtered on Maintenance and got a violet row would not know why.
 *
 * `deleted` is not a node status at all: it lists the tombstones of deleted
 * nodes whose devices may still be out there (`DeletedNode`), which the page
 * fetches from its own endpoint. Live nodes never match it, and tombstones
 * match nothing else.
 */
export type NodeStatusFilter =
  | 'all'
  | 'online'
  | 'recovery'
  | 'maintenance'
  | 'offline'
  | 'deleted'

// In menu order. The select renders from this list, so an option cannot exist
// in the menu without a meaning here.
export const NODE_STATUS_FILTERS: readonly NodeStatusFilter[] = [
  'all',
  'online',
  'recovery',
  'maintenance',
  'offline',
  'deleted',
]

export const isNodeStatusFilter = (value: string): value is NodeStatusFilter =>
  (NODE_STATUS_FILTERS as readonly string[]).includes(value)

const OFFLINE_STATUSES = ['offline', 'unreachable', 'shutdown']

/** The one non-`all` option a live node falls under. */
export const nodeStatusBucket = (
  node: NodeData,
): Exclude<NodeStatusFilter, 'all' | 'deleted'> => {
  if (isNetbootRecovery(node)) return 'recovery'
  const normalized = normalizeStatus(node.status || 'offline')
  if (OFFLINE_STATUSES.includes(normalized)) return 'offline'
  if (normalized === 'maintenance') return 'maintenance'
  return 'online'
}

/** Whether a live node is listed under the given option. */
export const matchesStatusFilter = (
  node: NodeData,
  filter: NodeStatusFilter,
): boolean => {
  if (filter === 'all') return true
  if (filter === 'deleted') return false
  return nodeStatusBucket(node) === filter
}

/** Whether the tombstones of deleted nodes are listed under the given option. */
export const statusFilterShowsDeleted = (filter: NodeStatusFilter): boolean =>
  filter === 'all' || filter === 'deleted'

/**
 * The text search over a deleted node, mirroring what the live-node search
 * reads where a tombstone still has it: name, hostname and serial. A tombstone
 * carries no address, OS or notes, so a query on those finds nothing — which is
 * correct, not a gap.
 */
export const deletedNodeMatchesQuery = (
  row: DeletedNode,
  query: string,
): boolean => {
  const needle = query.trim().toLowerCase()
  if (!needle) return true
  return [row.name, row.hostname, row.hardware_serial]
    .filter(Boolean)
    .join(' ')
    .toLowerCase()
    .includes(needle)
}
