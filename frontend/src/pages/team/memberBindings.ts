import type {
  BindingSpec,
  Member,
  MemberRole,
  RoleBinding,
} from '@/entities/member/api/types'

// A member can hold several role bindings: one account-wide, plus any number
// scoped to an organizational unit. The Members list shows a single role
// selector, which is fine as far as it goes — but the selector has to know that
// what it does not show still exists.
//
// Kept as a pure module so a test can import it without the store graph.

// accountWideRole is the role shown in the selector.
//
// It falls back to the first binding so a member who holds ONLY OU-scoped
// grants does not read as a viewer — which would be a lie about their access.
// bindingSummary is what tells the operator the difference.
export const accountWideRole = (member: Member): MemberRole => {
  const accountWide = member.bindings.find((b) => !b.ou_id)
  return accountWide?.role ?? member.bindings[0]?.role ?? 'viewer'
}

// nextBindings is what the selector must send: the account-wide role changed,
// every OU-scoped binding kept.
//
// The route is a full REPLACE, so sending [{role}] — which is what the selector
// used to do — silently deleted every unit-scoped grant the member had. Nothing
// on the page showed those bindings, so nobody could see what the change had
// cost, and the audit row records the replacement as one edit.
export const nextBindings = (
  member: Member,
  role: MemberRole,
): BindingSpec[] => {
  const scoped = member.bindings
    .filter((b) => b.ou_id)
    .map((b) => ({ role: b.role, ou_id: b.ou_id }))
  return [{ role }, ...scoped]
}

// ouScopedBindings are the bindings the selector does not represent.
export const ouScopedBindings = (member: Member): RoleBinding[] =>
  member.bindings.filter((b) => b.ou_id)

// hasAccountWideBinding reports whether the selector's value is a real binding
// or the fallback. A member with only OU-scoped grants has no account-wide
// role, and picking one in the selector ADDS access rather than changing it.
export const hasAccountWideBinding = (member: Member): boolean =>
  member.bindings.some((b) => !b.ou_id)
