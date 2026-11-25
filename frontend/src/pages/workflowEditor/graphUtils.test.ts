import { describe, expect, it } from 'vitest'

import type { WorkflowGraph } from '@/entities/workflow/api/types'

import {
  copyFragment,
  countRedactedParams,
  DEFAULT_PORT,
  emptyGraph,
  flowToGraph,
  GRAPH_SCHEMA_VERSION,
  graphToFlow,
  isPlaceableBlock,
  isTrigger,
  newNodeId,
  parseFragment,
  pasteFragment,
  REDACTED_PARAM_PLACEHOLDER,
} from './graphUtils'

// The canvas round-trip is what the editor SAVES: a field graphToFlow drops or
// flowToGraph forgets silently corrupts every playbook edited after it.
describe('graph <-> flow round trip', () => {
  const graph: WorkflowGraph = {
    schema_version: GRAPH_SCHEMA_VERSION,
    nodes: [
      { id: 'start', type: 'trigger.start', position: { x: 10, y: 20 } },
      {
        id: 'cond',
        type: 'control.condition',
        name: 'Check',
        description: 'branches',
        position: { x: 100, y: 20 },
        params: { left: '{{node.name}}', op: 'eq', right: 'a' },
        policy: { retries: 2, timeout_seconds: 30, on_error: 'fail' },
      },
      { id: 'rb', type: 'command.reboot', position: { x: 200, y: 20 } },
    ],
    edges: [
      { id: 'e1', from: 'start', to: 'cond', from_port: 'out' },
      { id: 'e2', from: 'cond', to: 'rb', from_port: 'true' },
    ],
  }

  it('survives graph -> flow -> graph unchanged where it matters', () => {
    const { nodes, edges } = graphToFlow(graph)
    const back = flowToGraph(nodes, edges)

    expect(back.schema_version).toBe(GRAPH_SCHEMA_VERSION)
    expect(back.nodes.map((n) => n.id)).toEqual(['start', 'cond', 'rb'])
    const cond = back.nodes.find((n) => n.id === 'cond')!
    expect(cond.params).toEqual(graph.nodes[1].params)
    expect(cond.name).toBe('Check')
    expect(cond.policy?.retries).toBe(2)
    expect(back.edges).toEqual([
      { id: 'e1', from: 'start', to: 'cond', from_port: 'out' },
      { id: 'e2', from: 'cond', to: 'rb', from_port: 'true' },
    ])
  })

  it('keeps non-default ports on the React Flow sourceHandle', () => {
    const { edges } = graphToFlow(graph)
    expect(edges[1].sourceHandle).toBe('true')
    // Non-default ports are labeled so the canvas shows which branch an edge is.
    expect(edges[1].label).toBe('true')
    expect(edges[0].label).toBeUndefined()
  })

  it('defaults a missing from_port to the default port', () => {
    const bare: WorkflowGraph = {
      schema_version: GRAPH_SCHEMA_VERSION,
      nodes: graph.nodes.slice(0, 2),
      edges: [{ id: 'e1', from: 'start', to: 'cond' }],
    }
    const { nodes, edges } = graphToFlow(bare)
    expect(flowToGraph(nodes, edges).edges[0].from_port).toBe(DEFAULT_PORT)
  })

  it('marks triggers non-deletable', () => {
    const { nodes } = graphToFlow(graph)
    expect(nodes.find((n) => n.id === 'start')!.deletable).toBe(false)
    expect(nodes.find((n) => n.id === 'rb')!.deletable).toBe(true)
  })
})

describe('helpers', () => {
  it('isTrigger matches the trigger namespace only', () => {
    expect(isTrigger('trigger.start')).toBe(true)
    expect(isTrigger('trigger.schedule')).toBe(true)
    expect(isTrigger('command.reboot')).toBe(false)
  })

  it('newNodeId never repeats, even for ids minted in one millisecond', () => {
    const seen = new Set<string>()
    for (let i = 0; i < 1000; i++) {
      const id = newNodeId()
      expect(seen.has(id)).toBe(false)
      seen.add(id)
    }
  })

  it('emptyGraph starts with the literal start trigger', () => {
    const { nodes, edges } = emptyGraph()
    expect(nodes).toHaveLength(1)
    expect(nodes[0].id).toBe('start')
    expect(edges).toHaveLength(0)
  })
})

// The save guard's scan: a placeholder anywhere in a param — top level, inside
// a list, inside a nested map — must be counted, or a redacted read gets
// written back over the real value.
describe('countRedactedParams', () => {
  const makeNode = (params: Record<string, unknown>) =>
    ({
      id: 'n1',
      position: { x: 0, y: 0 },
      data: { blockType: 'agent.run_script', params },
    }) as Parameters<typeof countRedactedParams>[0][number]

  it('counts placeholder values wherever they hide', () => {
    const nodes = [
      makeNode({
        script: REDACTED_PARAM_PLACEHOLDER,
        nested: { deep: REDACTED_PARAM_PLACEHOLDER },
        listed: [REDACTED_PARAM_PLACEHOLDER],
        clean: 'echo ok',
        num: 5,
      }),
    ]
    expect(countRedactedParams(nodes)).toBe(3)
  })

  it('returns zero for a clean canvas', () => {
    expect(countRedactedParams([makeNode({ script: 'echo ok' })])).toBe(0)
  })
})

// Clipboard fragments: each assertion below is one of the clipboard rules
// (a)–(e) in docs/PLAYBOOK-SUBSYSTEM-REVIEW.md §4.2 — skipping any of them is
// a bug, not a style choice.
describe('graph fragments', () => {
  const flowNode = (
    id: string,
    blockType: string,
    params: Record<string, unknown> = {},
    selected = true,
    x = 0,
  ) =>
    ({
      id,
      type: 'block' as const,
      position: { x, y: 0 },
      selected,
      data: { blockType, params },
    }) as Parameters<typeof copyFragment>[0][number]

  const canvas = () => {
    const trigger = flowNode('start', 'trigger.start', {}, false)
    const tail = flowNode('block-tail', 'agent.run_script', {}, false, 200)
    const edges = [
      {
        id: 'e1',
        source: 'start',
        target: 'block-tail',
        sourceHandle: DEFAULT_PORT,
      },
    ]
    return { existing: [trigger, tail], edges }
  }

  it('copies only selected nodes and the edges among them', () => {
    const a = flowNode('a', 'agent.run_script')
    const b = flowNode('b', 'agent.reboot')
    const outside = flowNode('c', 'agent.reboot', {}, false)
    const fragment = copyFragment(
      [a, b, outside],
      [
        { id: 'e1', source: 'a', target: 'b' },
        { id: 'e2', source: 'b', target: 'c' },
      ],
    )
    expect(fragment?.nodes.map((n) => n.id)).toEqual(['a', 'b'])
    expect(fragment?.edges).toHaveLength(1)
  })

  it('drops the trigger and its edges on paste (constraint e)', () => {
    const { existing, edges } = canvas()
    const fragment = copyFragment(
      [flowNode('t', 'trigger.start'), flowNode('a', 'agent.run_script')],
      [{ id: 'e', source: 't', target: 'a' }],
    )!
    const result = pasteFragment(fragment, existing, edges, {})!
    expect(result.nodes).toHaveLength(1)
    // The trigger's edge went with it; the surviving edge is the auto-connect
    // from the chain tail.
    expect(result.edges).toHaveLength(1)
    expect(result.edges[0].source).toBe('block-tail')
  })

  it('re-mints node AND edge ids (constraints c/d)', () => {
    const { existing, edges } = canvas()
    const fragment = copyFragment(
      [flowNode('a', 'agent.run_script'), flowNode('b', 'agent.reboot')],
      [{ id: 'dup-edge', source: 'a', target: 'b' }],
    )!
    const result = pasteFragment(fragment, existing, edges, {})!
    const ids = result.nodes.map((n) => n.id)
    expect(ids).not.toContain('a')
    expect(ids).not.toContain('b')
    expect(result.edges.map((e) => e.id)).not.toContain('dup-edge')
  })

  it('rewrites {{blocks.<id>}} references structurally, never textually (constraint c)', () => {
    const { existing, edges } = canvas()
    const fragment = copyFragment(
      [
        flowNode('block-1', 'agent.run_script'),
        flowNode('block-12', 'agent.run_script', {
          script:
            'echo {{blocks.block-1.outputs.stdout}} {{blocks.block-12.outputs.stdout}} {{secrets.block-1}} start',
        }),
      ],
      [],
    )!
    const result = pasteFragment(fragment, existing, edges, {})!
    const pasted = result.nodes.find(
      (n) => (n.data.params.script as string | undefined) !== undefined,
    )!
    const script = pasted.data.params.script as string
    const idOf = (oldId: string) =>
      result.nodes[fragment.nodes.findIndex((n) => n.id === oldId)].id
    // block-1 rewritten inside its OWN segment; block-12 not corrupted by the
    // block-1 prefix; a secrets ref sharing the id text untouched; the bare
    // word "start" untouched.
    expect(script).toContain(`{{blocks.${idOf('block-1')}.outputs.stdout}}`)
    expect(script).toContain(`{{blocks.${idOf('block-12')}.outputs.stdout}}`)
    expect(script).toContain('{{secrets.block-1}}')
    expect(script.endsWith('start')).toBe(true)
  })

  it('auto-connects every entry node to the chain tail (constraint a)', () => {
    const { existing, edges } = canvas()
    const fragment = copyFragment(
      [flowNode('a', 'agent.run_script'), flowNode('b', 'agent.reboot')],
      [],
    )!
    const result = pasteFragment(fragment, existing, edges, {})!
    const sources = result.edges.map((e) => e.source)
    expect(sources).toEqual(['block-tail', 'block-tail'])
  })

  it('checks pasted from_port values against the block outputs (constraint e)', () => {
    const { existing, edges } = canvas()
    const fragment = copyFragment(
      [flowNode('c', 'control.condition'), flowNode('a', 'agent.run_script')],
      [{ id: 'e', source: 'c', target: 'a', sourceHandle: 'not-a-port' }],
    )!
    const result = pasteFragment(fragment, existing, edges, {
      'control.condition': ['true', 'false'],
    })!
    // The bad-port edge dropped with a warning; both nodes became entries and
    // auto-connected to the tail.
    expect(result.warnings.some((w) => w.includes('not-a-port'))).toBe(true)
    expect(result.edges.every((e) => e.source === 'block-tail')).toBe(true)
  })

  it('keeps param values verbatim — clearing install_os.image_id is destructive, not inert (constraint b)', () => {
    const { existing, edges } = canvas()
    const fragment = copyFragment(
      [flowNode('i', 'agent.install_os', { image_id: 'not-in-this-account' })],
      [],
    )!
    const result = pasteFragment(fragment, existing, edges, {})!
    expect(result.nodes[0].data.params.image_id).toBe('not-in-this-account')
  })

  it('parseFragment rejects foreign clipboard text', () => {
    expect(parseFragment('hello world')).toBeNull()
    expect(parseFragment('{"kind":"something-else"}')).toBeNull()
    const fragment = copyFragment([flowNode('a', 'agent.run_script')], [])!
    expect(parseFragment(JSON.stringify(fragment))?.nodes).toHaveLength(1)
  })
})

describe('isPlaceableBlock', () => {
  // The boot-order block saves cleanly and is refused on every run.
  it('leaves out triggers and the boot-order block', () => {
    expect(isPlaceableBlock({ type: 'trigger.start' })).toBe(false)
    expect(
      isPlaceableBlock({
        type: 'command.update_boot_order',
        slug: 'update_boot_order',
      }),
    ).toBe(false)
    expect(isPlaceableBlock({ type: 'command.reboot', slug: 'reboot' })).toBe(
      true,
    )
  })
})
