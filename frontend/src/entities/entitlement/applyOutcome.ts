import type { ApplyEntitlementResult } from './api'

// What the card says after a paste the server accepted. Accepted is not the
// same as in force: the install runs under the newest issued file that has not
// lapsed, so a pasted file can be stored and still be outranked by another, or
// arrive already lapsed. A response without `applied` reads as a plain success.
export type ApplyOutcome = {
  // 'success' is a passing toast; 'notice' stays on the card until the next
  // paste, because the operator has to act on it.
  tone: 'success' | 'notice'
  key: 'applied' | 'appliedNotInForce' | 'appliedLapsed'
  values: Record<string, string>
}

export const applyOutcome = (
  result: ApplyEntitlementResult,
  formatDate: (iso: string | undefined) => string,
): ApplyOutcome => {
  const applied = result.applied
  if (!applied) return { tone: 'success', key: 'applied', values: {} }
  const expires = formatDate(applied.expires_at)
  if (applied.state === 'lapsed') {
    return { tone: 'notice', key: 'appliedLapsed', values: { expires } }
  }
  if (!applied.in_force) {
    return { tone: 'notice', key: 'appliedNotInForce', values: { expires } }
  }
  return { tone: 'success', key: 'applied', values: {} }
}
