import { describe, expect, it } from 'vitest'

import type { Member, MemberRole } from '@/entities/member/api/types'

import {
  accountWideRole,
  hasAccountWideBinding,
  nextBindings,
  ouScopedBindings,
} from './memberBindings'

const member = (
  bindings: Array<{ role: MemberRole; ou_id?: string }>,
): Member => ({
  user_id: 'u1',
  name: 'Sam',
  email: 'sam@example.com',
  status: 'active',
  bindings: bindings.map((b, i) => ({ id: `b${i}`, ...b })),
  created_at: '2026-08-19T00:00:00Z',
})

describe('memberBindings', () => {
  // The defect: the route is a full replace, so sending just the selector's
  // value deleted every unit-scoped grant, with nothing on the page showing
  // that they had existed.
  it('keeps every OU-scoped binding when the account-wide role changes', () => {
    const m = member([
      { role: 'viewer' },
      { role: 'admin', ou_id: 'ou-1' },
      { role: 'manager', ou_id: 'ou-2' },
    ])

    expect(nextBindings(m, 'manager')).toEqual([
      { role: 'manager' },
      { role: 'admin', ou_id: 'ou-1' },
      { role: 'manager', ou_id: 'ou-2' },
    ])
  })

  it('adds the account-wide binding for a member who had none', () => {
    const m = member([{ role: 'admin', ou_id: 'ou-1' }])
    expect(hasAccountWideBinding(m)).toBe(false)
    expect(nextBindings(m, 'viewer')).toEqual([
      { role: 'viewer' },
      { role: 'admin', ou_id: 'ou-1' },
    ])
  })

  // Falling back to the first binding rather than to 'viewer': a member holding
  // only unit-scoped admin is not a viewer, and showing them as one is a lie
  // about their access.
  it('does not report an OU-scoped admin as a viewer', () => {
    expect(accountWideRole(member([{ role: 'admin', ou_id: 'ou-1' }]))).toBe(
      'admin',
    )
    expect(accountWideRole(member([]))).toBe('viewer')
  })

  it('lists exactly the bindings the selector does not represent', () => {
    const m = member([{ role: 'viewer' }, { role: 'admin', ou_id: 'ou-1' }])
    expect(ouScopedBindings(m).map((b) => b.ou_id)).toEqual(['ou-1'])
  })
})
