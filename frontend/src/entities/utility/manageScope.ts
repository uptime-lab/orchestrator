// Whether the caller may change an assignment, by the server's rule: an
// assignment on units needs the right on EVERY one of them (it is a change to
// each), one on a node needs it on the node's unit, and an account-wide or
// unassigned one needs it account-wide. A member who manages one unit can
// therefore manage that unit's assignments, which a bare account-wide check
// refused.
export type ScopedCheck = (ouId?: string | null) => boolean

export const canManageAssignment = (
  check: ScopedCheck,
  assignment: {
    ou_ids?: string[]
    ou_id?: string
    node_id?: string
  },
  nodeOUId: (nodeId: string) => string | null | undefined,
): boolean => {
  const units =
    assignment.ou_ids && assignment.ou_ids.length > 0
      ? assignment.ou_ids
      : assignment.ou_id
        ? [assignment.ou_id]
        : []
  if (units.length > 0) return units.every((ou) => check(ou))
  if (assignment.node_id) {
    const ou = nodeOUId(assignment.node_id)
    return ou ? check(ou) : check()
  }
  return check()
}
