import {
  Background,
  BackgroundVariant,
  Handle,
  Position,
  ReactFlow,
  type Node,
  type NodeProps,
  type NodeTypes,
} from '@xyflow/react'
import { memo, useMemo } from 'react'

import '@xyflow/react/dist/style.css'

import clsx from 'clsx'

import type {
  WorkflowGraph,
  WorkflowGraphNodeState,
} from '@/entities/workflow/api/types'
import { useWorkflowBlocks } from '@/entities/workflow/model'

import { graphToFlow, isTrigger } from './graphUtils'

type RunNodeData = {
  blockType: string
  label: string
  status?: string
  [key: string]: unknown
}
type RunNode = Node<RunNodeData, 'run'>

const STATUS_RING: Record<string, string> = {
  running: 'border-orange-5 shadow-[0_0_10px_rgba(255,154,61,0.5)]',
  completed: 'border-green-6',
  failed: 'border-red-6',
  skipped: 'border-neutral-10 opacity-50',
}
const STATUS_DOT: Record<string, string> = {
  running: 'bg-orange-5 animate-pulse',
  completed: 'bg-green-6',
  failed: 'bg-red-6',
  skipped: 'bg-neutral-8',
}

const RunNodeView = memo(({ data }: NodeProps<RunNode>) => {
  const status = data.status || 'pending'
  const trigger = isTrigger(data.blockType)
  return (
    <div
      className={clsx(
        'rounded-md border bg-neutral-13 px-3 py-1.5 text-xs text-neutral-2',
        trigger ? 'rounded-full' : 'w-44',
        STATUS_RING[status] || 'border-neutral-11',
      )}
    >
      {!trigger && (
        <Handle
          type="target"
          position={Position.Top}
          className="!bg-neutral-8 !w-1.5 !h-1.5"
        />
      )}
      <div className="flex items-center gap-2">
        <span
          className={clsx(
            'w-2 h-2 rounded-full shrink-0',
            STATUS_DOT[status] || 'bg-neutral-7',
          )}
        />
        <span className="truncate">{data.label}</span>
      </div>
      <Handle
        type="source"
        position={Position.Bottom}
        className="!bg-neutral-8 !w-1.5 !h-1.5"
      />
    </div>
  )
})
RunNodeView.displayName = 'RunNodeView'

const nodeTypes: NodeTypes = { run: RunNodeView }

type RunGraphViewProps = {
  graph: WorkflowGraph
  graphState: Record<string, WorkflowGraphNodeState>
}

// Read-only canvas rendering a run's pinned graph, blocks colored by the
// selected node's per-block state (running/completed/failed/skipped).
export const RunGraphView = ({ graph, graphState }: RunGraphViewProps) => {
  const { blocks } = useWorkflowBlocks()

  const { nodes, edges } = useMemo(() => {
    const flow = graphToFlow(graph)
    const runNodes: RunNode[] = flow.nodes.map((n) => {
      const def = blocks.find((b) => b.type === n.data.blockType)
      return {
        id: n.id,
        type: 'run' as const,
        position: n.position,
        draggable: false,
        connectable: false,
        data: {
          blockType: n.data.blockType,
          label: n.data.name || def?.name || n.data.blockType,
          status: graphState[n.id]?.status,
        },
      }
    })
    return { nodes: runNodes, edges: flow.edges }
  }, [graph, graphState, blocks])

  return (
    <div className="h-64 rounded border border-neutral-11 bg-neutral-14">
      <ReactFlow
        nodes={nodes}
        edges={edges}
        nodeTypes={nodeTypes}
        colorMode="dark"
        fitView
        fitViewOptions={{ maxZoom: 1, padding: 0.15 }}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        panOnScroll={false}
        zoomOnScroll={false}
        proOptions={{ hideAttribution: true }}
      >
        <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
      </ReactFlow>
    </div>
  )
}
