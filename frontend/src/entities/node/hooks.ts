import { useStoreMap, useUnit } from 'effector-react'

import { NodeData } from '@/entities/node/api/types.ts'

import { $nodeCommands, $nodes, $nodesById } from './model'

export const useNodes = () => {
  const nodes = useUnit($nodes)

  return { nodes }
}

export const useNode = (params: { nodeId: string }) => {
  const node: NodeData | null = useStoreMap({
    store: $nodesById,
    keys: [params.nodeId],
    fn: (nodesById, [nodeId]) => nodesById[nodeId] ?? null,
  })

  return { node }
}

export const useNodeCommands = (nodeId: string) => {
  const commands = useStoreMap({
    store: $nodeCommands,
    keys: [nodeId],
    fn: (allCommands, [id]) => allCommands[id] || [],
  })
  return commands
}
