// agentVersionLess mirrors the backend's node.AgentVersionLess, the one order
// the CDN listing and the fleet's "agent outdated" check use: dotted segments,
// numeric where both are numbers ("0.2.9" < "0.2.10"), lexicographic
// otherwise, and a prefix of a longer version sorts lower. A second order here
// that disagreed would offer an update the Status page calls current.
export const agentVersionLess = (a: string, b: string): boolean => {
  const as = a.split('.')
  const bs = b.split('.')
  for (let i = 0; i < as.length && i < bs.length; i++) {
    if (as[i] === bs[i]) continue
    const an = /^\d+$/.test(as[i]) ? Number(as[i]) : NaN
    const bn = /^\d+$/.test(bs[i]) ? Number(bs[i]) : NaN
    if (!Number.isNaN(an) && !Number.isNaN(bn)) return an < bn
    return as[i] < bs[i]
  }
  return as.length < bs.length
}

// What the agent row offers for a node, given what `latest` installs.
//   update  - latest is newer, or either side is unknown (the row as before)
//   current - the node runs latest
//   ahead   - the node runs a newer version than latest; an update would be
//             a downgrade, so nothing is offered
export type AgentUpdateState = 'update' | 'current' | 'ahead'

export const agentUpdateState = (
  version: string | undefined,
  latest: string | null,
): AgentUpdateState => {
  const have = version?.trim()
  const want = latest?.trim()
  if (!have || !want) return 'update'
  if (have === want) return 'current'
  return agentVersionLess(have, want) ? 'update' : 'ahead'
}
