// pruneSelection keeps the selected ids that still name a node in the current
// list. A node deleted while it was selected stays in the selection with no
// row to deselect it by, and the server refuses a whole playbook run over one
// unknown target, so a batch action sends only what is still there and says
// how many it left out.
export const pruneSelection = (
  selected: Iterable<string>,
  nodes: readonly { id: string }[],
): { kept: string[]; dropped: number } => {
  const present = new Set(nodes.map((n) => n.id))
  const kept: string[] = []
  let dropped = 0
  for (const id of selected) {
    if (present.has(id)) kept.push(id)
    else dropped += 1
  }
  return { kept, dropped }
}

type NotFoundLike = {
  response?: { status?: unknown; data?: { message?: unknown } }
}

// The start route's answer when one target is not a node of the account,
// byte-compared against the server's message; its other 404 names the
// playbook instead.
const NODE_NOT_FOUND_MESSAGE = 'node not found'

// isStaleTargetRefusal reads a playbook start refused because a selected
// node went away between the pruning and the start.
export const isStaleTargetRefusal = (error: unknown): boolean => {
  const response = (error as NotFoundLike | undefined)?.response
  return (
    response?.status === 404 &&
    response.data?.message === NODE_NOT_FOUND_MESSAGE
  )
}
