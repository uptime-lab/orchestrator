// The concrete version a one-click update installs.
//
// The Update button dispatches `agent_version: 'latest'`, so the node must be
// compared against what `latest` IS, not against the newest version listed: a
// release imported without moving `latest` sits at the top of the list while
// every update still installs the previous one. The server reports that
// version as `latest` (the import records it beside the `latest` objects);
// an older backend without the field falls back to the newest listed version.
export const latestAgentVersion = (data: unknown): string | null => {
  if (!data || typeof data !== 'object') return null
  const { latest, versions } = data as {
    latest?: unknown
    versions?: unknown
  }
  if (typeof latest === 'string' && latest.trim() && latest !== 'latest') {
    return latest
  }
  if (!Array.isArray(versions)) return null
  const concrete = versions.filter(
    (v): v is string => typeof v === 'string' && v !== 'latest',
  )
  return concrete[0] ?? null
}
