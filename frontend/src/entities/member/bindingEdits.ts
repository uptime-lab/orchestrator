import { writeFromCurrent } from '@/shared/utils/writeFromCurrent'

import type { BindingSpec, Member, MemberRole } from './api/types'

// The bindings route replaces a member's whole set, so every edit is computed
// from the member as the server holds it at the moment of writing (see
// writeFromCurrent). Each builder returns null when, against that state, the
// edit has nothing to do.

const asSpecs = (member: Member): BindingSpec[] =>
  member.bindings.map((b) => ({ role: b.role, ou_id: b.ou_id }))

// Grant `role` on one unit, keeping every other binding. Null when the member
// already holds a role on that unit: a second grant there would not be the
// change the operator saw offered.
export const withUnitGrant = (
  member: Member,
  role: MemberRole,
  ouId: string,
): BindingSpec[] | null => {
  if (member.bindings.some((b) => b.ou_id === ouId)) return null
  return [...asSpecs(member), { role, ou_id: ouId }]
}

// Revoke the member's role on one unit, keeping every other binding. Matched
// by unit and role rather than by binding id, because a replace made by
// someone else re-issues the ids.
export const withoutUnitGrant = (
  member: Member,
  role: MemberRole,
  ouId: string,
): BindingSpec[] | null => {
  const kept = member.bindings.filter(
    (b) => !(b.ou_id === ouId && b.role === role),
  )
  if (kept.length === member.bindings.length) return null
  return kept.map((b) => ({ role: b.role, ou_id: b.ou_id }))
}

// The member as listed now, or null when they are no longer a member.
export const findMember = (members: Member[], userId: string): Member | null =>
  members.find((m) => m.user_id === userId) ?? null

// The refusal code of a bindings write whose expected_binding_ids no longer
// match the member's bindings.
export const MEMBER_BINDINGS_STALE = 'member_bindings_stale'

// Raised before writing when the member's bindings are no longer the ones the
// operator was shown.
export class MemberBindingsStaleError extends Error {
  constructor() {
    super(MEMBER_BINDINGS_STALE)
    this.name = 'MemberBindingsStaleError'
  }
}

// Whether a bindings write was refused because the member's roles changed
// since the control was filled, by this client's own check or the server's.
export const isMemberBindingsStale = (err: unknown): boolean => {
  if (err instanceof MemberBindingsStaleError) return true
  const response = (
    err as { response?: { status?: number; data?: { code?: unknown } } }
  )?.response
  return (
    response?.status === 409 && response.data?.code === MEMBER_BINDINGS_STALE
  )
}

const idsOf = (member: Member): string[] =>
  member.bindings.map((b) => b.id).sort()

const sameBindings = (a: Member, b: Member): boolean => {
  const x = idsOf(a)
  const y = idsOf(b)
  return x.length === y.length && x.every((id, i) => id === y[i])
}

export type MemberBindingsApi = {
  listMembers: () => Promise<Member[]>
  replaceBindings: (
    userId: string,
    bindings: BindingSpec[],
    expectedBindingIds: string[],
  ) => Promise<unknown>
}

// Writes one edit of a member's bindings. `seen` is the member as the control
// showing them was filled. The member is read again first; if their bindings
// are no longer the ones shown, nothing is written (MemberBindingsStaleError).
// The write carries the shown binding ids as expected_binding_ids, so the
// server refuses it as well if they change between the read and the write.
// Never retried: the operator decides again on what is there now.
export const editMemberBindings = (
  seen: Member,
  build: (current: Member) => BindingSpec[] | null,
  api: MemberBindingsApi,
) =>
  writeFromCurrent(
    async () => findMember(await api.listMembers(), seen.user_id),
    (current) => {
      if (!current || !sameBindings(current, seen)) {
        throw new MemberBindingsStaleError()
      }
      return build(current)
    },
    (bindings) =>
      api.replaceBindings(
        seen.user_id,
        bindings,
        seen.bindings.map((b) => b.id),
      ),
  )
