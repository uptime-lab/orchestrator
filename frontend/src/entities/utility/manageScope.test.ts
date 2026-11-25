import { describe, expect, it } from 'vitest'

import { canManageAssignment } from './manageScope'

// A member who manages unit A only.
const check = (ou?: string | null) => ou === 'A'
const nodeOU = (id: string) => (id === 'n-in-A' ? 'A' : null)

describe('canManageAssignment', () => {
  it('lets a unit manager change the assignments on their unit', () => {
    expect(canManageAssignment(check, { ou_ids: ['A'] }, nodeOU)).toBe(true)
    expect(canManageAssignment(check, { node_id: 'n-in-A' }, nodeOU)).toBe(true)
  })

  it('needs every unit of a multi-unit assignment', () => {
    expect(canManageAssignment(check, { ou_ids: ['A', 'B'] }, nodeOU)).toBe(
      false,
    )
  })

  it('needs the account-wide right for account-wide or unassigned ones', () => {
    expect(canManageAssignment(check, {}, nodeOU)).toBe(false)
    expect(canManageAssignment(check, { node_id: 'n-loose' }, nodeOU)).toBe(
      false,
    )
  })
})
