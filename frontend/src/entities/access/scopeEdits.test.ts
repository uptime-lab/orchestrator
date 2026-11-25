import { describe, expect, it } from 'vitest'

import type { AccessEntry } from './api/types'
import {
  withPrincipalAdded,
  withPrincipalRemoved,
  withUnitAdded,
  withUnitRemoved,
} from './scopeEdits'

const entry = (over: Partial<AccessEntry>): AccessEntry => ({
  id: 'e1',
  account_id: 'a1',
  type: 'user',
  scope: 'ou',
  ou_ids: [],
  account_wide: false,
  username: 'deploy',
  sudo: false,
  has_password: false,
  adopt_unmanaged: false,
  key_ids: [],
  created_at: '',
  updated_at: '',
  ...over,
})

describe('withUnitAdded', () => {
  // A user on every node dropped on a unit used to be re-scoped to that one
  // unit, which locks it on every other node.
  it('leaves a user on every node alone', () => {
    expect(
      withUnitAdded(entry({ scope: 'account', account_wide: true }), 'ou-a'),
    ).toBeNull()
  })

  it('adds the unit to the units the user is in now', () => {
    expect(withUnitAdded(entry({ ou_ids: ['ou-b'] }), 'ou-a')).toEqual({
      scope: { ou_ids: ['ou-b', 'ou-a'] },
    })
  })

  it('does nothing for a unit the user is already in, or a node-bound user', () => {
    expect(withUnitAdded(entry({ ou_ids: ['ou-a'] }), 'ou-a')).toBeNull()
    expect(
      withUnitAdded(entry({ scope: 'node', node_id: 'n1' }), 'ou-a'),
    ).toBeNull()
  })
})

describe('withUnitRemoved', () => {
  it('keeps the other units', () => {
    expect(withUnitRemoved(entry({ ou_ids: ['a', 'b'] }), 'a')).toEqual({
      scope: { ou_ids: ['b'] },
    })
    expect(withUnitRemoved(entry({ ou_ids: ['b'] }), 'a')).toBeNull()
  })
})

describe('principals', () => {
  it('adds and removes one principal against the current set', () => {
    expect(withPrincipalAdded(entry({ principals: ['ops'] }), 'web')).toEqual({
      principals: ['ops', 'web'],
    })
    expect(withPrincipalAdded(entry({ principals: ['ops'] }), 'ops')).toBeNull()
    expect(
      withPrincipalRemoved(entry({ principals: ['ops', 'web'] }), 'ops'),
    ).toEqual({ principals: ['web'] })
  })
})
