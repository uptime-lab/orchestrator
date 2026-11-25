import { describe, expect, it } from 'vitest'

import { can, canAnyScope } from './can'

// canAnyScope backs NAVIGATION only, and the distinction from can() is
// load-bearing: can() with no target requires an account-wide grant, so gating
// the nav on it would hide every section from an OU-scoped member — a worse
// outcome than the one the gating fixes.
describe('canAnyScope', () => {
  const ou = '7c9e6679-7425-40de-944b-e07fc1f90ae7'

  it('grants an account-wide holder', () => {
    expect(canAnyScope({ 'secret.read': '*' }, 'secret.read')).toBe(true)
  })

  it('grants an OU-scoped holder, where can() with no target refuses', () => {
    const perms = { 'node.read': [ou] }
    expect(canAnyScope(perms, 'node.read')).toBe(true)
    expect(can(perms, 'node.read')).toBe(false)
  })

  it('refuses an action the map does not carry at all', () => {
    expect(canAnyScope({ 'node.read': '*' }, 'secret.read')).toBe(false)
  })

  // A failed permission load leaves the map empty, and hiding everything is the
  // correct fail-closed answer — the same one can() gives.
  it('refuses everything on an empty map', () => {
    expect(canAnyScope({}, 'node.read')).toBe(false)
  })

  // An empty OU list means the action is held in no scope at all. It must not
  // read as "held somewhere" just because the key exists.
  it('refuses an action held in no scope', () => {
    expect(canAnyScope({ 'secret.read': [] }, 'secret.read')).toBe(false)
  })
})

// The backend accepts an OU-scoped binding for four catalog reads at the
// account level (authz.anyScopeReadActions). Until this shipped the frontend
// was strictly narrower than the server on exactly those four: it hid
// affordances the backend would have allowed, which an operator reads as a
// permission they do not have.
describe('the account-level catalog reads', () => {
  const scoped = {
    'ou.read': ['ou-1'],
    'image.read': ['ou-1'],
    'sshkey.read': ['ou-1'],
    'favorite.set': ['ou-1'],
    'node.read': ['ou-1'],
    'secret.manage': ['ou-1'],
  }

  it('lets an OU-scoped binding answer an account-level read', () => {
    expect(can(scoped, 'ou.read')).toBe(true)
    expect(can(scoped, 'image.read')).toBe(true)
    expect(can(scoped, 'sshkey.read')).toBe(true)
    expect(can(scoped, 'favorite.set')).toBe(true)
  })

  // Everything else keeps the strict rule, and every mutation does regardless:
  // widening beyond the four would let an OU-scoped member act account-wide.
  it('keeps every other action account-wide for a nil target', () => {
    expect(can(scoped, 'node.read')).toBe(false)
    expect(can(scoped, 'secret.manage')).toBe(false)
  })

  // A binding that carries the action in NO scope is not a grant. The backend
  // omits such actions, so this should not arrive — but presence alone meaning
  // "yes" would put a button in front of a 403.
  it('does not accept an empty scope list', () => {
    expect(can({ 'ou.read': [] }, 'ou.read')).toBe(false)
  })

  // The targeted question is unchanged: a scoped grant covers its own units and
  // no others.
  it('still answers a targeted check by the scope list', () => {
    expect(can(scoped, 'ou.read', 'ou-1')).toBe(true)
    expect(can(scoped, 'ou.read', 'ou-2')).toBe(false)
  })
})
