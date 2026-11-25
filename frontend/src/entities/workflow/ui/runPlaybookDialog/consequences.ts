// What a run will DO, read off the playbook's own blocks.
//
// `dangerous` and `reboots_node` reach the API and were rendered at AUTHORING
// time only — in the editor, to the person writing the playbook. The operator
// deciding whether to run it on twenty machines saw neither, against a starter
// catalog whose entries are destructive OS work.
//
// Kept apart from the component so a test can import it without the store graph,
// which reaches `window` at import time.

export type BlockConsequence = {
  type: string
  name: string
  dangerous: boolean
  reboots_node: boolean
}

export type RunConsequences = { dangerous: string[]; reboots: string[] }

// runConsequences names the DISTINCT blocks in the graph that destroy data or
// reboot the node. Names, not slugs: the operator reading this never saw the
// registry.
//
// A block the registry does not know is skipped rather than guessed at. That is
// the honest direction: the catalog is the source of truth for what a block
// does, and inventing a consequence for an unknown one would be a warning about
// nothing.
export const runConsequences = (
  graphNodeTypes: string[],
  blocksByType: Record<string, BlockConsequence | undefined>,
): RunConsequences => {
  const dangerous = new Set<string>()
  const reboots = new Set<string>()
  graphNodeTypes.forEach((type) => {
    const block = blocksByType[type]
    if (!block) return
    if (block.dangerous) dangerous.add(block.name)
    if (block.reboots_node) reboots.add(block.name)
  })
  return { dangerous: [...dangerous], reboots: [...reboots] }
}

export type RunGate = 'loading' | 'needs-acknowledgement' | 'ready'

// runGate decides whether a run may start. Until the block registry has
// loaded, what the playbook does is unknown, so nothing starts: reading an
// empty registry as "no consequences" let a destructive playbook run with no
// acknowledgement. A playbook with a destructive block then needs the tick.
export const runGate = (
  registryLoaded: boolean,
  consequences: RunConsequences,
  acknowledged: boolean,
): RunGate => {
  if (!registryLoaded) return 'loading'
  if (consequences.dangerous.length > 0 && !acknowledged) {
    return 'needs-acknowledgement'
  }
  return 'ready'
}
