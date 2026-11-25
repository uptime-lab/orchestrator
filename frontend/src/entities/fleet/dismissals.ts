// Dismissing an attention condition, keyed by VALUE rather than by a clock.
//
// The rule is the one entities/node/markDismissal already settled for the
// button mark, and for the same reason: a dismissal that expires on a timer
// hides a real problem for exactly as long as nobody is watching, while a
// dismissal keyed to WHAT was dismissed comes back the moment the situation
// changes. "I know about 0.6.13 → 0.7.1" must not also mean "I know about
// 0.7.1 → 0.8.0".
//
// Per browser, not per account. A shared dismissal is a product decision with
// a table behind it — who may hide a condition from their colleagues, and how
// it is un-hidden — and this is the smaller, reversible half: the page always
// offers a way to see what is hidden and put it back.
//
// Kept as a pure module so a test can import it without the store graph.

import type { AttentionItem } from './api'

const STORAGE_KEY = 'status-attention-dismissals'

/** What was dismissed, by `nodeId:kind`, holding the value dismissed. */
export type Dismissals = Record<string, string>

export const dismissalKey = (nodeId: string, kind: string) =>
  `${nodeId}:${kind}`

/**
 * The part of a condition that makes it THIS occurrence rather than a later
 * one. A version pair moves when a new release lands; a subject names the
 * utility or the slug; a count moves as boot attempts accumulate.
 *
 * Conditions with none of these — a node in recovery, hardware declared and
 * never applied — dismiss as a bare fact and stay dismissed until they clear
 * and return. That is the honest reading of "hide this", and the page's
 * show-dismissed toggle is what keeps it from being a one-way door.
 */
export const dismissalValue = (item: AttentionItem): string =>
  item.expected ||
  item.subject ||
  (item.count ? String(item.count) : '') ||
  'set'

export const isDismissed = (
  dismissals: Dismissals,
  nodeId: string,
  item: AttentionItem,
): boolean => {
  const recorded = dismissals[dismissalKey(nodeId, item.kind)]
  if (recorded === undefined) {
    return false
  }
  // Strictly equal. A different value is a different occurrence and must show.
  return recorded === dismissalValue(item)
}

export const withDismissal = (
  dismissals: Dismissals,
  nodeId: string,
  item: AttentionItem,
): Dismissals => ({
  ...dismissals,
  [dismissalKey(nodeId, item.kind)]: dismissalValue(item),
})

export const withoutDismissal = (
  dismissals: Dismissals,
  nodeId: string,
  kind: string,
): Dismissals => {
  const next = { ...dismissals }
  delete next[dismissalKey(nodeId, kind)]
  return next
}

/**
 * Reads and writes are try/catch'd on purpose: a private window, cleared site
 * data or a browser set to block storage must cost the operator a forgotten
 * dismissal, never a page that will not render.
 */
export const readDismissals = (): Dismissals => {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? (JSON.parse(raw) as Dismissals) : {}
  } catch {
    return {}
  }
}

export const writeDismissals = (dismissals: Dismissals): void => {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(dismissals))
  } catch {
    // Nothing to do and nothing worth telling the operator: the dismissal
    // simply does not outlive the page.
  }
}

/**
 * Per-kind totals with this browser's hidden conditions removed.
 *
 * One implementation for two readers that must agree: the nav badge (summed)
 * and the Status page's filter chips. Split in two, the visible failure is
 * "I hid it and the number did not move", which reads as a broken button.
 *
 * Deliberately NOT the server's `counts`, which knows nothing about a
 * dismissal — hiding is per browser, and asking the server to model it would
 * be a table for a preference.
 */
export const undismissedCountsByKind = (
  nodes: { node_id: string; items: AttentionItem[] }[],
  dismissals: Dismissals,
): Record<string, number> => {
  const counts: Record<string, number> = {}
  for (const node of nodes) {
    for (const item of node.items) {
      if (!isDismissed(dismissals, node.node_id, item)) {
        counts[item.kind] = (counts[item.kind] ?? 0) + 1
      }
    }
  }
  return counts
}

/** The same set, summed — what the nav badge shows. */
export const undismissedTotal = (
  nodes: { node_id: string; items: AttentionItem[] }[],
  dismissals: Dismissals,
): number =>
  Object.values(undismissedCountsByKind(nodes, dismissals)).reduce(
    (total, n) => total + n,
    0,
  )
