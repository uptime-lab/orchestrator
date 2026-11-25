import type { AccessEntry, UpdateAccessEntryRequest } from './api/types'

// A scope or principals change replaces the whole list, so it is computed from
// the entry as the server holds it now (see editAccessEntry). Each builder
// returns null when, against that state, there is nothing to change.

// Add one unit to the entry's units. Null for an entry that already reaches
// every node or is bound to a single node: adding a unit to either would not
// add anything — it would replace what the entry reaches.
export const withUnitAdded = (
  entry: AccessEntry,
  ouId: string,
): UpdateAccessEntryRequest | null => {
  if (entry.account_wide || entry.scope === 'account') return null
  if (entry.scope === 'node') return null
  if (entry.ou_ids.includes(ouId)) return null
  return { scope: { ou_ids: [...entry.ou_ids, ouId] } }
}

// Remove one unit from the entry's units, keeping the others.
export const withUnitRemoved = (
  entry: AccessEntry,
  ouId: string,
): UpdateAccessEntryRequest | null => {
  if (!entry.ou_ids.includes(ouId)) return null
  return { scope: { ou_ids: entry.ou_ids.filter((id) => id !== ouId) } }
}

export const withPrincipalAdded = (
  entry: AccessEntry,
  principal: string,
): UpdateAccessEntryRequest | null => {
  const current = entry.principals ?? []
  if (current.includes(principal)) return null
  return { principals: [...current, principal] }
}

export const withPrincipalRemoved = (
  entry: AccessEntry,
  principal: string,
): UpdateAccessEntryRequest | null => {
  const current = entry.principals ?? []
  if (!current.includes(principal)) return null
  return { principals: current.filter((p) => p !== principal) }
}
