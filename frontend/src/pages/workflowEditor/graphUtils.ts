import type { Edge, Node } from '@xyflow/react'
import { t } from 'i18next'

// Side-effect init: this module is imported by tests (and could be imported by
// any consumer) without the app shell, and an uninitialised i18next t()
// returns undefined instead of the message. In the app this is a no-op —
// main.tsx already evaluated the same module.
import '@/i18n'

import type {
  WorkflowBlock,
  WorkflowBlockPolicy,
  WorkflowGraph,
  WorkflowGraphNode,
} from '@/entities/workflow/api/types'

export const START_TYPE = 'trigger.start'
export const SCHEDULE_TYPE = 'trigger.schedule'
export const JOIN_TYPE = 'control.join'
export const DEFAULT_PORT = 'out'
export const GRAPH_SCHEMA_VERSION = 1

export const isTrigger = (blockType: string) => blockType.startsWith('trigger.')

// Blocks the palette does not offer. "Update boot order" saves, but every run
// of it is refused: the device accepts a boot order only inside a signed
// bootloader package, which this block cannot carry. A playbook that already
// has one keeps it (it still loads and shows); a new one cannot be added.
const UNRUNNABLE_BLOCK_SLUGS = new Set(['update_boot_order'])

// isPlaceableBlock is what the palette lists: every block but the triggers
// (created with the graph) and the unrunnable ones above.
export const isPlaceableBlock = (block: { type: string; slug?: string }) =>
  !isTrigger(block.type) && !UNRUNNABLE_BLOCK_SLUGS.has(block.slug ?? '')

export type BlockNodeData = {
  blockType: string
  name?: string
  description?: string
  params: Record<string, unknown>
  policy?: WorkflowBlockPolicy
  [key: string]: unknown
}

export type BlockNode = Node<BlockNodeData, 'block'>

let idCounter = 0
export const newNodeId = () => {
  idCounter += 1
  return `block-${Date.now().toString(36)}-${idCounter}`
}

// Backend graph -> React Flow nodes/edges. The edge's from_port maps to the
// React Flow sourceHandle so multi-port blocks (condition true/false) round-trip.
export const graphToFlow = (
  graph: WorkflowGraph,
): { nodes: BlockNode[]; edges: Edge[] } => {
  const nodes: BlockNode[] = graph.nodes.map((node) => ({
    id: node.id,
    type: 'block' as const,
    position: { x: node.position?.x ?? 0, y: node.position?.y ?? 0 },
    deletable: !isTrigger(node.type),
    data: {
      blockType: node.type,
      name: node.name,
      description: node.description,
      params: (node.params as Record<string, unknown>) || {},
      policy: node.policy,
    },
  }))
  const edges: Edge[] = graph.edges.map((edge, index) => ({
    id: edge.id || `edge-${index}`,
    source: edge.from,
    target: edge.to,
    sourceHandle: edge.from_port || DEFAULT_PORT,
    label:
      edge.from_port && edge.from_port !== DEFAULT_PORT
        ? edge.from_port
        : undefined,
  }))
  return { nodes, edges }
}

// React Flow nodes/edges -> backend graph.
// The literal the server substitutes for free-text param values when the
// reader lacks secret.read_value. Must match the backend's
// workflow_template.RedactedParamPlaceholder — the save guard scans for it so
// a redacted read is never written back over the real value (the server
// refuses it too; catching it here keeps the draft in the editor).
export const REDACTED_PARAM_PLACEHOLDER =
  '[redacted — requires secret.read_value]'

const valueCarriesPlaceholder = (value: unknown): boolean => {
  if (typeof value === 'string')
    return value.includes(REDACTED_PARAM_PLACEHOLDER)
  if (Array.isArray(value)) return value.some(valueCarriesPlaceholder)
  if (value && typeof value === 'object') {
    return Object.values(value as Record<string, unknown>).some(
      valueCarriesPlaceholder,
    )
  }
  return false
}

// countRedactedParams counts param fields still carrying the placeholder
// across the canvas, for the save guard's message.
export const countRedactedParams = (nodes: BlockNode[]): number => {
  let count = 0
  for (const node of nodes) {
    for (const value of Object.values(node.data.params)) {
      if (valueCarriesPlaceholder(value)) count += 1
    }
  }
  return count
}

export const flowToGraph = (
  nodes: BlockNode[],
  edges: Edge[],
): WorkflowGraph => ({
  schema_version: GRAPH_SCHEMA_VERSION,
  nodes: nodes.map((node): WorkflowGraphNode => {
    const graphNode: WorkflowGraphNode = {
      id: node.id,
      type: node.data.blockType,
      position: { x: node.position.x, y: node.position.y },
    }
    if (node.data.name) graphNode.name = node.data.name
    if (node.data.description) graphNode.description = node.data.description
    if (Object.keys(node.data.params).length > 0) {
      graphNode.params = node.data.params
    }
    if (node.data.policy && hasPolicy(node.data.policy)) {
      graphNode.policy = node.data.policy
    }
    return graphNode
  }),
  edges: edges.map((edge) => ({
    id: edge.id,
    from: edge.source,
    to: edge.target,
    from_port: (edge.sourceHandle as string) || DEFAULT_PORT,
  })),
})

const hasPolicy = (p: WorkflowBlockPolicy): boolean =>
  !!p.retries || !!p.timeout_seconds || (!!p.on_error && p.on_error !== 'fail')

export const emptyGraph = (): { nodes: BlockNode[]; edges: Edge[] } => ({
  nodes: [
    {
      id: 'start',
      type: 'block',
      position: { x: 120, y: 60 },
      deletable: false,
      data: { blockType: START_TYPE, params: {} },
    },
  ],
  edges: [],
})

// Client-side mirror of the server-side validation: trigger present, every
// node reachable, required params filled. The server remains the authority
// (returns 400) — this is for immediate feedback.
export const validateFlow = (
  nodes: BlockNode[],
  edges: Edge[],
  blocksByType: Record<string, WorkflowBlock>,
): string[] => {
  const errors: string[] = []

  const triggers = nodes.filter((n) => isTrigger(n.data.blockType))
  if (triggers.length === 0) {
    errors.push(t('pages.workflowEditor.graph.noTrigger'))
    return errors
  }
  if (triggers.length > 1) {
    errors.push(t('pages.workflowEditor.graph.multipleTriggers'))
  }
  if (nodes.length === 1) {
    errors.push(t('pages.workflowEditor.graph.onlyTrigger'))
  }

  // Reachability from the trigger over all ports (BFS).
  const adjacency = new Map<string, string[]>()
  edges.forEach((edge) => {
    adjacency.set(edge.source, [
      ...(adjacency.get(edge.source) || []),
      edge.target,
    ])
  })
  const visited = new Set<string>([triggers[0].id])
  const queue = [triggers[0].id]
  while (queue.length > 0) {
    const current = queue.shift() as string
    for (const next of adjacency.get(current) || []) {
      if (!visited.has(next)) {
        visited.add(next)
        queue.push(next)
      }
    }
  }
  nodes.forEach((node) => {
    if (!visited.has(node.id)) {
      const block = blocksByType[node.data.blockType]
      const label = node.data.name || block?.name || node.data.blockType
      errors.push(t('pages.workflowEditor.graph.notConnected', { label }))
    }
  })

  return errors
}

// Missing required params per the registry, reported separately from the
// structural errors: they block saving an ACTIVE playbook but a draft may be
// saved half-filled (the backend applies the same draft relaxation).
export const missingParamErrors = (
  nodes: BlockNode[],
  blocksByType: Record<string, WorkflowBlock>,
): string[] => {
  const errors: string[] = []
  nodes.forEach((node) => {
    const block = blocksByType[node.data.blockType]
    if (!block) return
    const label = node.data.name || block.name
    block.params.forEach((param) => {
      if (!param.required) return
      const value = node.data.params[param.name]
      const missing =
        value === undefined ||
        value === null ||
        value === '' ||
        (Array.isArray(value) && value.length === 0)
      if (missing) {
        errors.push(
          t('pages.workflowEditor.graph.paramRequired', {
            label,
            param: param.label,
          }),
        )
      }
    })
  })
  return errors
}

// Interactive connect rule mirroring the graph model: no edges into a trigger,
// no self-loops, no cycles; a condition port carries at most one edge, the
// default port may fan out; only Join accepts more than one incoming edge.
export const canConnect = (
  sourceId: string,
  sourceHandle: string | null,
  targetId: string,
  nodes: BlockNode[],
  edges: Edge[],
): boolean => {
  if (sourceId === targetId) return false
  const target = nodes.find((n) => n.id === targetId)
  if (!target || isTrigger(target.data.blockType)) return false

  const port = sourceHandle || DEFAULT_PORT
  // A non-default (condition) port carries a single edge.
  if (
    port !== DEFAULT_PORT &&
    edges.some(
      (e) => e.source === sourceId && (e.sourceHandle || DEFAULT_PORT) === port,
    )
  ) {
    return false
  }
  // Only Join accepts more than one incoming edge.
  if (
    target.data.blockType !== JOIN_TYPE &&
    edges.some((e) => e.target === targetId)
  ) {
    return false
  }

  // Walking forward from the target must not reach the source (cycle).
  const adjacency = new Map<string, string[]>()
  edges.forEach((e) =>
    adjacency.set(e.source, [...(adjacency.get(e.source) || []), e.target]),
  )
  const seen = new Set<string>()
  const stack = [targetId]
  while (stack.length > 0) {
    const current = stack.pop() as string
    if (current === sourceId) return false
    if (seen.has(current)) continue
    seen.add(current)
    stack.push(...(adjacency.get(current) || []))
  }
  return true
}

// ---------------------------------------------------------------------------
// Clipboard fragments (the rules every paste holds:
// docs/PLAYBOOK-SUBSYSTEM-REVIEW.md §4.2). The unit is the selected nodes
// plus the edges among them; whole-playbook copy is select-all.

export const FRAGMENT_CLIPBOARD_KIND = 'orchestrator-playbook-fragment'

export type GraphFragment = {
  kind: typeof FRAGMENT_CLIPBOARD_KIND
  version: 1
  nodes: WorkflowGraphNode[]
  edges: WorkflowGraph['edges']
}

// copyFragment serializes the selected nodes and the edges AMONG them. The
// trigger is copyable (a select-all should round-trip); paste is what drops
// it — the receiving canvas already has one.
export const copyFragment = (
  nodes: BlockNode[],
  edges: Edge[],
): GraphFragment | null => {
  const selected = nodes.filter((n) => n.selected)
  if (selected.length === 0) return null
  const ids = new Set(selected.map((n) => n.id))
  return {
    kind: FRAGMENT_CLIPBOARD_KIND,
    version: 1,
    nodes: selected.map((node) => ({
      id: node.id,
      type: node.data.blockType,
      position: { x: node.position.x, y: node.position.y },
      ...(node.data.name ? { name: node.data.name } : {}),
      ...(node.data.description ? { description: node.data.description } : {}),
      ...(Object.keys(node.data.params).length > 0
        ? { params: node.data.params }
        : {}),
      ...(node.data.policy && hasPolicy(node.data.policy)
        ? { policy: node.data.policy }
        : {}),
    })),
    edges: edges
      .filter((e) => ids.has(e.source) && ids.has(e.target))
      .map((e) => ({
        id: e.id,
        from: e.source,
        to: e.target,
        from_port:
          e.sourceHandle && e.sourceHandle !== DEFAULT_PORT
            ? e.sourceHandle
            : undefined,
      })),
  }
}

// rewriteBlockRefs rewrites {{blocks.<id>.outputs.*}} references
// STRUCTURALLY: each {{...}} expression is parsed the way the backend
// resolver parses it (split on '.'), and only the id SEGMENT is replaced.
// A textual substring replace would corrupt values — "block-1" is a strict
// prefix of "block-12", and the trigger id is the literal "start", which is
// also a manage_service.action value.
const rewriteBlockRefs = (
  value: unknown,
  idMap: Map<string, string>,
): unknown => {
  if (typeof value === 'string') {
    return value.replace(/\{\{([^}]*)\}\}/g, (match, inner: string) => {
      const parts = inner.trim().split('.')
      if (parts[0] === 'blocks' && parts.length >= 2 && idMap.has(parts[1])) {
        parts[1] = idMap.get(parts[1])!
        return `{{${parts.join('.')}}}`
      }
      return match
    })
  }
  if (Array.isArray(value)) return value.map((v) => rewriteBlockRefs(v, idMap))
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      out[k] = rewriteBlockRefs(v, idMap)
    }
    return out
  }
  return value
}

export type PasteResult = {
  nodes: BlockNode[]
  edges: Edge[]
  warnings: string[]
}

// pasteFragment turns a clipboard fragment into ready-to-add nodes/edges:
// triggers dropped with their edges, every id re-minted (nodes AND edges —
// React Flow's edgeLookup silently drops a duplicate edge id that
// flowToGraph would still emit), block references rewritten structurally,
// from_port values checked against the source block's declared outputs, and
// every entry node auto-connected to the chain tail — a pasted fragment is
// otherwise unreachable from the trigger, and reachability is checked in the
// shared Validate, so NO save of any kind would succeed.
//
// Param values are deliberately NOT cleared, even when they reference
// entities this account cannot resolve: an empty install_os.image_id is not
// inert — it falls back to the node's effective OU/account image at dispatch
// — so a cleared reference silently becomes a DIFFERENT destructive action.
// An unresolvable value fails at save/run with its own message instead.
export const pasteFragment = (
  fragment: GraphFragment,
  existingNodes: BlockNode[],
  existingEdges: Edge[],
  outputsByBlockType: Record<string, string[] | undefined>,
): PasteResult | null => {
  const kept = fragment.nodes.filter((n) => !isTrigger(n.type))
  if (kept.length === 0) return null
  const warnings: string[] = []
  const droppedTriggers = fragment.nodes.length - kept.length
  if (droppedTriggers > 0) {
    warnings.push(t('pages.workflowEditor.graph.triggerNotPasted'))
  }

  const idMap = new Map<string, string>()
  kept.forEach((n) => idMap.set(n.id, newNodeId()))

  // Translate the fragment so it lands beside the existing flow instead of on
  // top of its source position, keeping the fragment's own layout.
  const minX = Math.min(...kept.map((n) => n.position?.x ?? 0))
  const minY = Math.min(...kept.map((n) => n.position?.y ?? 0))
  const maxExistingX = existingNodes.length
    ? Math.max(...existingNodes.map((n) => n.position.x))
    : 0
  const baseX = maxExistingX + 260
  const baseY = existingNodes.length
    ? Math.min(...existingNodes.map((n) => n.position.y))
    : 0

  const nodes: BlockNode[] = kept.map((n) => ({
    id: idMap.get(n.id)!,
    type: 'block' as const,
    position: {
      x: baseX + ((n.position?.x ?? 0) - minX),
      y: baseY + ((n.position?.y ?? 0) - minY),
    },
    deletable: true,
    selected: true,
    data: {
      blockType: n.type,
      name: n.name,
      description: n.description,
      params:
        (rewriteBlockRefs(n.params ?? {}, idMap) as Record<string, unknown>) ||
        {},
      policy: n.policy,
    },
  }))

  const typeOf = new Map(kept.map((n) => [idMap.get(n.id)!, n.type]))
  const edges: Edge[] = []
  for (const e of fragment.edges ?? []) {
    const from = idMap.get(e.from)
    const to = idMap.get(e.to)
    // An edge touching a dropped trigger (or anything outside the fragment)
    // goes with it.
    if (!from || !to) continue
    const port = e.from_port || DEFAULT_PORT
    if (port !== DEFAULT_PORT) {
      const declared = outputsByBlockType[typeOf.get(from) ?? '']
      if (declared && !declared.includes(port)) {
        warnings.push(
          t('pages.workflowEditor.graph.unknownPortDropped', {
            type: typeOf.get(from),
            port,
          }),
        )
        continue
      }
    }
    edges.push({
      id: `edge-${newNodeId()}`,
      source: from,
      target: to,
      sourceHandle: port,
      label: port !== DEFAULT_PORT ? port : undefined,
    })
  }

  // Reachability: connect every entry node (no incoming edge INSIDE the
  // fragment) to the current chain tail.
  const hasIncoming = new Set(edges.map((e) => e.target))
  const entries = nodes.filter((n) => !hasIncoming.has(n.id))
  const tail = chainTail(existingNodes, existingEdges)
  if (tail) {
    entries.forEach((entry) => {
      edges.push({
        id: `edge-${newNodeId()}`,
        source: tail.id,
        target: entry.id,
        sourceHandle: DEFAULT_PORT,
      })
    })
  } else if (entries.length > 0) {
    warnings.push(t('pages.workflowEditor.graph.connectPasted'))
  }

  return { nodes, edges, warnings }
}

// parseFragment validates clipboard text into a fragment, or null when it is
// not ours — ordinary text paste must fall through untouched.
export const parseFragment = (text: string): GraphFragment | null => {
  if (!text || !text.includes(FRAGMENT_CLIPBOARD_KIND)) return null
  try {
    const parsed = JSON.parse(text) as GraphFragment
    if (
      parsed?.kind !== FRAGMENT_CLIPBOARD_KIND ||
      !Array.isArray(parsed.nodes)
    ) {
      return null
    }
    return parsed
  } catch {
    return null
  }
}

// The free end of the flow a newly-added block auto-connects to: the last
// block with no outgoing edge on a linear tail from the trigger. Returns
// undefined when the tail branches (the user wires it manually then).
export const chainTail = (
  nodes: BlockNode[],
  edges: Edge[],
): BlockNode | undefined => {
  const trigger = nodes.find((n) => isTrigger(n.data.blockType))
  if (!trigger) return undefined
  const outgoing = new Map<string, string[]>()
  edges.forEach((e) =>
    outgoing.set(e.source, [...(outgoing.get(e.source) || []), e.target]),
  )
  let current = trigger
  const seen = new Set<string>()
  while (!seen.has(current.id)) {
    seen.add(current.id)
    const next = outgoing.get(current.id) || []
    if (next.length === 0) return current
    if (next.length > 1) return undefined // branch point — manual wiring
    const nextNode = nodes.find((n) => n.id === next[0])
    if (!nextNode) return current
    current = nextNode
  }
  return undefined
}

// --- boot-state warnings ---------------------------------------------------

// What we believe the node is running at a point in the graph. `unknown` is the
// starting belief and the honest one: a playbook may legitimately be started on
// a node that is ALREADY netbooted — that is how every install flow works — and
// no static reading of the graph can know which.
export type BootBelief = 'unknown' | 'recovery' | 'managed_os'

const REBOOT_TO_RECOVERY = 'server.reboot_to_recovery'
const WAIT_FOR_NODE = 'control.wait_for_node'
const REBOOT = 'command.reboot'

// beliefAfter applies one block to the current belief.
//
// Only WAIT_FOR_NODE actually establishes anything: a reboot proves the node is
// going down, not what it comes back as. So a reboot ERASES the belief, and the
// wait that follows sets it — which is also why the wait's `state` param is
// read rather than its presence: its default is `online`, which proves nothing
// about the boot mode.
const beliefAfter = (
  blockType: string,
  params: Record<string, unknown>,
  current: BootBelief,
): BootBelief => {
  if (blockType === WAIT_FOR_NODE) {
    const state = typeof params.state === 'string' ? params.state : 'online'
    if (state === 'recovery') return 'recovery'
    if (state === 'managed_os') return 'managed_os'
    return current
  }
  if (blockType === REBOOT_TO_RECOVERY || blockType === REBOOT) return 'unknown'
  return current
}

// bootWarnings reports blocks placed where the graph says the node is in the
// wrong state. It walks every path from the trigger, so a block reached two
// ways is judged on each.
//
// It warns ONLY on a contradiction, never on `unknown`. That single rule is
// what keeps it useful: warning on uncertainty would fire on every playbook
// that starts from recovery — including three of the four built-in starters —
// and a warning that fires on correct work is one nobody reads.
export const bootWarnings = (
  nodes: BlockNode[],
  edges: Edge[],
  blocksByType: Record<string, WorkflowBlock>,
  // The belief the walk starts from. The editor knows nothing about a future
  // run and passes 'unknown'; the RUN dialog knows exactly what the chosen node
  // is doing right now and passes that, which turns the same walk into "will
  // this playbook work on THIS node" — one implementation, two questions.
  initialBelief: BootBelief = 'unknown',
): string[] => {
  const trigger = nodes.find((n) => isTrigger(n.data.blockType))
  if (!trigger) return []

  const byId = new Map(nodes.map((n) => [n.id, n]))
  const outgoing = new Map<string, string[]>()
  edges.forEach((edge) => {
    outgoing.set(edge.source, [
      ...(outgoing.get(edge.source) || []),
      edge.target,
    ])
  })

  const warnings: string[] = []
  const reported = new Set<string>()
  // Visited is keyed on node AND belief: the same block reached with a
  // different belief is a different question, while an identical revisit is a
  // loop guard.
  const visited = new Set<string>()
  const queue: Array<{ id: string; belief: BootBelief }> = [
    { id: trigger.id, belief: initialBelief },
  ]

  while (queue.length > 0) {
    const { id, belief } = queue.shift()!
    const key = `${id}:${belief}`
    if (visited.has(key)) continue
    visited.add(key)

    const node = byId.get(id)
    if (!node) continue
    const block = blocksByType[node.data.blockType]
    const requirement = block?.boot_requirement

    if (block && !reported.has(node.id)) {
      const label = node.data.name || block.name
      if (requirement === 'recovery' && belief === 'managed_os') {
        reported.add(node.id)
        warnings.push(
          t('pages.workflowEditor.graph.needsRecoveryWarning', { label }),
        )
      }
      if (requirement === 'managed_os' && belief === 'recovery') {
        reported.add(node.id)
        warnings.push(
          t('pages.workflowEditor.graph.needsManagedOsWarning', { label }),
        )
      }
    }

    const next = beliefAfter(
      node.data.blockType,
      node.data.params || {},
      belief,
    )
    ;(outgoing.get(id) || []).forEach((targetId) => {
      queue.push({ id: targetId, belief: next })
    })
  }

  return warnings
}
