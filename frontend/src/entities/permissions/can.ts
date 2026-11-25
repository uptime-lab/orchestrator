import { PermissionMap } from './api'

// The two permission predicates, kept apart from the effector stores so they
// can be read — and tested — without pulling in the store graph and everything
// it touches at import time.

// canAnyScope reports whether the user holds an action ANYWHERE — account-wide
// or in any OU. It is the right question for NAVIGATION, and only for that: a
// nav item asks "does this section exist for me", not "may I do this to that
// target". can() answers the second question and must stay the gate on every
// action.
//
// The distinction is load-bearing here. can() with no target requires an
// account-wide grant, so gating the nav on it would hide EVERY section from an
// OU-scoped member — a much worse outcome than the one being fixed. The backend
// draws the same line for its catalog reads (authz.IsAnyScopeRead).
export const canAnyScope = (perms: PermissionMap, action: string): boolean => {
  const scope = perms[action]
  if (scope === undefined) return false
  // An empty OU list is held in no scope at all. The backend omits actions the
  // user cannot perform anywhere, so this should not arrive — but a key whose
  // presence alone meant "yes" would put a nav item in front of a 403, which is
  // the failure this gating exists to remove.
  return scope === '*' || scope.length > 0
}

// ANY_SCOPE_READS mirrors authz.anyScopeReadActions on the backend: catalog
// reads an OU-scoped binding satisfies at the ACCOUNT level (a nil target).
//
// They are the four an OU-scoped member needs in order to interpret their own
// subtree — the unit tree itself (the list handler narrows it afterwards), the
// image and SSH-key catalogs OU policies reference by id, and the personal
// favourites view. Without this list the frontend was strictly narrower than
// the server: it hid affordances the backend would have allowed, which reads to
// the operator as a permission they do not have.
//
// Copied deliberately rather than derived. The permission map is a per-action
// scope, not a rule set, so there is nothing to derive it from — and the
// backend's own comment says widening the set is a product decision.
//
// The copy is pinned from the OTHER side: authz.TestFrontendAnyScopeReadsMatch
// reads this file and compares. It lives there because the check needs to read
// Go source, and the browser tsconfig deliberately does not expose Node's fs —
// a component that could import it is a worse problem than the one this guards.
// It caught a real drift on the first run: this list said `ssh_key.read` while
// the action is `sshkey.read`, so the SSH-key half of the fix did nothing.
export const ANY_SCOPE_READS = new Set([
  'ou.read',
  'image.read',
  'sshkey.read',
  'favorite.set',
  'utility.read',
])

export const can = (
  perms: PermissionMap,
  action: string,
  ouId?: string | null,
): boolean => {
  const scope = perms[action]
  if (scope === undefined) return false
  if (scope === '*') return true
  // OU-scoped grant with no target. The strict rule is that only an
  // account-wide grant covers it — except for the catalog reads above, where
  // the backend accepts any binding carrying the action.
  if (!ouId) return ANY_SCOPE_READS.has(action) && scope.length > 0
  return scope.includes(ouId)
}
