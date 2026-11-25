import {
  combine,
  createEffect,
  createEvent,
  createStore,
  sample,
} from 'effector'
import { useUnit } from 'effector-react'

import { $preferHostnames, setPreferHostnames } from '../user/preferences'
import { userCommonSocketModel } from '../userCommonSocket'
import { apiNodes } from './api'
import type { NodeCommand, NodeData, NodeMetricsSnapshot } from './api/types'
import { nodeDisplayName } from './utils'

// ============= Types =============

// ============= Stores =============

export const $nodes = createStore<NodeData[]>([])

// Derived store: map of nodes by ID for efficient lookups
export const $nodesById = combine($nodes, (nodes) => {
  const map: Record<string, NodeData> = {}
  nodes.forEach((node) => {
    map[node.id] = node
  })
  return map
})

export const $nodeCommands = createStore<Record<string, NodeCommand[]>>({})

// ============= Effects =============

export const fetchNodesFx = createEffect(() => apiNodes.getNodes())

// Resync the fleet when the socket reconnects after a drop: a node that changed
// state while the socket was down produced no event, so its row is stale until a
// refetch. This is the refetch the reconciliation in `.on(fetchNodesFx.doneData)`
// below was written for.
sample({
  clock: userCommonSocketModel.events.userSocketReconnected,
  target: fetchNodesFx,
})

export const fetchNodeCommandsFx = createEffect(async (nodeId: string) => {
  return await apiNodes.getNodeCommands(nodeId, 100, 0) // Fetch up to 100 commands
})

// ============= Favorites =============

// Per-user favorite node ids (a personal view; no policy semantics).
export const $favoriteNodeIds = createStore<Set<string>>(new Set())

export const fetchFavoritesFx = createEffect(async () => {
  return await apiNodes.getFavoriteNodeIds()
})

export const toggleFavoriteFx = createEffect(
  async ({ nodeId, favorite }: { nodeId: string; favorite: boolean }) => {
    return await apiNodes.putNodeFavorite(nodeId, favorite)
  },
)

$favoriteNodeIds
  .on(fetchFavoritesFx.doneData, (_, ids) => new Set(ids))
  .on(toggleFavoriteFx.doneData, (_, ids) => new Set(ids))

export const useFavoriteNodeIds = (): Set<string> => useUnit($favoriteNodeIds)

// ============= Side effects =============

// Default order: the displayed name, which depends on the "prefer hostnames"
// preference — a store sorted by the hidden name reads as unsorted.
const byDisplayName =
  (preferHostnames: boolean) => (a: NodeData, b: NodeData) =>
    nodeDisplayName(a, preferHostnames).localeCompare(
      nodeDisplayName(b, preferHostnames),
    )

// Resolved ONCE per sort, not per comparison: sortNodes() reads the store
// and returns the comparator, so a 200-node re-sort costs one getState()
// rather than O(n log n) of them.
const sortNodes = () => byDisplayName($preferHostnames.getState())

// $nodesLoadFailed separates "this account has no nodes" from "we could not
// ask". Both leave $nodes empty, and the list then renders the "add your first
// node" invitation — shown to a customer who has a fleet, it reads as data
// loss. The layout loader uses Promise.allSettled and nothing subscribed to
// this effect's failure, so the request simply vanished.
export const $nodesLoadFailed = createStore(false)

$nodesLoadFailed
  .on(fetchNodesFx.fail, () => true)
  .on(fetchNodesFx.doneData, () => false)

$nodes
  .on(fetchNodesFx.doneData, (_, nodes) => [...nodes].sort(sortNodes()))
  // Re-order in place when the display preference flips: the payload carries
  // the new value because $preferHostnames may not have updated yet within
  // the same event batch.
  .on(setPreferHostnames, (nodes, preferHostnames) =>
    [...nodes].sort(byDisplayName(preferHostnames)),
  )
  .on(
    userCommonSocketModel.events.userSocketOnNodeCreated,
    (nodes, { payload }) => {
      // Convert NodeResult to NodeData (NodeResult is compatible with NodeData)
      const nodeData = payload as NodeData
      const exists = nodes.some((node) => node.id === nodeData.id)
      if (exists) {
        return [...nodes].sort(sortNodes())
      }
      return [...nodes, nodeData].sort(sortNodes())
    },
  )
  .on(
    userCommonSocketModel.events.userSocketOnNodeUpdated,
    (nodes, { payload }) => {
      // Convert NodeResult to NodeData
      const nodeData = payload as NodeData
      const updatedNodes = nodes.map((node) =>
        node.id === nodeData.id ? nodeData : node,
      )
      return [...updatedNodes].sort(sortNodes())
    },
  )
  .on(
    userCommonSocketModel.events.userSocketOnNodeDeleted,
    (nodes, { payload }) =>
      nodes.filter((node) => node.id !== payload.id).sort(sortNodes()),
  )

// Update node commands store when commands are created or updated
$nodeCommands
  .on(
    userCommonSocketModel.events.userSocketOnNodeCommandCreated,
    (commands, { payload }) => {
      const nodeId = payload.node_id
      const existing = commands[nodeId] || []
      // Check if command already exists (avoid duplicates)
      if (existing.some((cmd) => cmd.id === payload.id)) {
        return commands
      }
      return {
        ...commands,
        [nodeId]: [...existing, payload].sort(
          (a, b) =>
            new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
        ),
      }
    },
  )
  .on(
    userCommonSocketModel.events.userSocketOnNodeCommandUpdated,
    (commands, { payload }) => {
      const nodeId = payload.node_id
      const existing = commands[nodeId] || []
      // MERGE, never replace: the pushed frame carries a redacted payload, and
      // overwriting would discard the real one a privileged caller already
      // fetched over HTTP.
      const updated = existing.map((cmd) =>
        cmd.id === payload.id ? { ...cmd, ...payload } : cmd,
      )
      return {
        ...commands,
        [nodeId]: updated.sort(
          (a, b) =>
            new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
        ),
      }
    },
  )

// Event for updating commands from API fetch
const nodeCommandsFetched = createEvent<{
  nodeId: string
  commands: NodeCommand[]
}>()

// Update node commands store when fetching from API
sample({
  clock: fetchNodeCommandsFx.done,
  fn: ({ params: nodeId, result: fetchedCommands }) => ({
    nodeId,
    commands: fetchedCommands.sort(
      (a, b) =>
        new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
    ),
  }),
  target: nodeCommandsFetched,
})

// Update store when commands are fetched from API
$nodeCommands.on(
  nodeCommandsFetched,
  (currentCommands, { nodeId, commands }) => ({
    ...currentCommands,
    [nodeId]: commands,
  }),
)

// ============= Live metrics =============

// Bounded per-node ring of recent samples for sparklines. Fed by the
// node_metrics WebSocket event; kept separate from $nodes so a 5s sample does
// not replace/re-sort the whole node list.
const METRICS_HISTORY = 60
export const $nodeMetrics = createStore<Record<string, NodeMetricsSnapshot[]>>(
  {},
)
  .on(
    userCommonSocketModel.events.userSocketOnNodeMetrics,
    (state, { payload }) => {
      const prev = state[payload.node_id] ?? []
      const next = [...prev, payload.metrics].slice(-METRICS_HISTORY)
      return { ...state, [payload.node_id]: next }
    },
  )
  // Drop a node's samples the moment it stops being online.
  //
  // Samples are a LIVE reading, and the ring kept the last ones forever — so a
  // node that went offline carried on showing a CPU percentage and a
  // temperature, indefinitely, with nothing marking them stale. That is the
  // worst version of the mistake: not missing data, but plausible data that is
  // no longer true.
  //
  // Cleared here in the store rather than in each view. MetricsPanel already
  // has correct copy for the offline case ("No live metrics — node is
  // offline"), but it only reaches that branch when the ring is EMPTY, so the
  // stale samples were exactly what kept the right message from rendering. The
  // fleet card gets the same fix for free, and neither can regress
  // independently.
  .on(
    userCommonSocketModel.events.userSocketOnNodeUpdated,
    (state, { payload }) => {
      const node = payload as { id: string; status?: string }
      if (node.status === 'online' || !state[node.id]) {
        return state
      }
      const next = { ...state }
      delete next[node.id]
      return next
    },
  )
  .on(
    userCommonSocketModel.events.userSocketOnNodeDeleted,
    (state, { payload }) => {
      if (!state[payload.id]) {
        return state
      }
      const next = { ...state }
      delete next[payload.id]
      return next
    },
  )
  // The status can also arrive by a plain list refetch rather than by a socket
  // event — a node that went offline while the socket was down produces no
  // update event at all, and reconnecting refetches instead. Without this the
  // stale ring survives exactly the case the WebSocket could not report.
  .on(fetchNodesFx.doneData, (state, nodes) => {
    const online = new Set(
      nodes.filter((n) => n.status === 'online').map((n) => n.id),
    )
    const stale = Object.keys(state).filter((id) => !online.has(id))
    if (stale.length === 0) {
      return state
    }
    const next = { ...state }
    stale.forEach((id) => delete next[id])
    return next
  })

// ============= Hooks =============

export const useNodeMetrics = (nodeId: string): NodeMetricsSnapshot[] => {
  const all = useUnit($nodeMetrics)
  return all[nodeId] ?? []
}

export const useNodes = () => {
  const nodes = useUnit($nodes)
  const fetchNodes = useUnit(fetchNodesFx)
  return { nodes, fetchNodes }
}

export const useNodeCommands = (nodeId: string) => {
  const commands = useUnit($nodeCommands)
  return commands[nodeId] || []
}
