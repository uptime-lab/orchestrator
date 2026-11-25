import { describe, expect, it, vi } from 'vitest'

import type { Member, MemberRole } from './api/types'
import {
  editMemberBindings,
  isMemberBindingsStale,
  MemberBindingsStaleError,
  withoutUnitGrant,
  withUnitGrant,
} from './bindingEdits'

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

describe('editMemberBindings', () => {
  const api = (listed: Member[]) => ({
    listMembers: vi.fn(async () => listed),
    replaceBindings: vi.fn(async () => undefined),
  })

  // A unit grant made from a list cached before a demotion re-sent the old
  // account-wide role. The member is read again, and when their bindings are
  // no longer the ones shown, nothing is written.
  it('writes nothing when the roles changed since the control was filled', async () => {
    const shown = member([{ role: 'admin' }])
    const now = {
      ...member([{ role: 'viewer' }]),
      bindings: [{ id: 'b9', role: 'viewer' as MemberRole }],
    }
    const deps = api([now])
    await expect(
      editMemberBindings(
        shown,
        (m) => withUnitGrant(m, 'manager', 'ou-1'),
        deps,
      ),
    ).rejects.toBeInstanceOf(MemberBindingsStaleError)
    expect(deps.replaceBindings).not.toHaveBeenCalled()
  })

  it('sends the shown binding ids as the expected version', async () => {
    const shown = member([{ role: 'viewer' }, { role: 'admin', ou_id: 'ou-2' }])
    const deps = api([shown])
    await editMemberBindings(
      shown,
      (m) => withUnitGrant(m, 'manager', 'ou-1'),
      deps,
    )
    expect(deps.replaceBindings).toHaveBeenCalledWith(
      'u1',
      [
        { role: 'viewer', ou_id: undefined },
        { role: 'admin', ou_id: 'ou-2' },
        { role: 'manager', ou_id: 'ou-1' },
      ],
      ['b0', 'b1'],
    )
  })

  it('recognises the server refusing a stale write', () => {
    expect(
      isMemberBindingsStale({
        response: { status: 409, data: { code: 'member_bindings_stale' } },
      }),
    ).toBe(true)
    expect(isMemberBindingsStale({ response: { status: 409, data: {} } })).toBe(
      false,
    )
  })
})

describe('unit grants', () => {
  it('does not grant twice on one unit', () => {
    expect(
      withUnitGrant(
        member([{ role: 'viewer', ou_id: 'ou-1' }]),
        'admin',
        'ou-1',
      ),
    ).toBeNull()
  })

  it('revokes by unit and role, keeping the rest', () => {
    expect(
      withoutUnitGrant(
        member([
          { role: 'viewer' },
          { role: 'manager', ou_id: 'ou-1' },
          { role: 'admin', ou_id: 'ou-2' },
        ]),
        'manager',
        'ou-1',
      ),
    ).toEqual([
      { role: 'viewer', ou_id: undefined },
      { role: 'admin', ou_id: 'ou-2' },
    ])
  })

  it('writes nothing when the grant is already gone', () => {
    expect(
      withoutUnitGrant(member([{ role: 'viewer' }]), 'manager', 'ou-1'),
    ).toBeNull()
  })
})
