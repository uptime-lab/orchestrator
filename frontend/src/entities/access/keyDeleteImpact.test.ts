import { describe, expect, it } from 'vitest'

import type { AccessEntry } from './api/types'
import { keyDeleteImpact } from './keyDeleteImpact'

const user = (
  id: string,
  key_ids: string[],
  type: AccessEntry['type'] = 'user',
  has_password = false,
) => ({ id, type, key_ids, has_password }) as AccessEntry

describe('keyDeleteImpact', () => {
  it('counts the users who lose the key and those left with none', () => {
    expect(
      keyDeleteImpact(
        [
          user('a', ['k1']),
          user('b', ['k1', 'k2']),
          user('c', ['k2']),
          user('ca', ['k1'], 'ca'),
          // Keeps a password, so it stays active.
          user('d', ['k1'], 'user', true),
        ],
        'k1',
      ),
    ).toEqual({ users: 3, locked: 1 })
  })
})
