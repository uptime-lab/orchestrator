// The unit the replacement node belongs to once the identity has moved: the
// replace transfers the source's unit onto the target, so the source's unit
// wins when it has one, and the target keeps its own otherwise. The server
// tests a playbook's scope against exactly this unit, so the picker filters
// by it too — filtering by the target's current unit offers playbooks the
// server then refuses, and hides the ones it would run.
export const replacementUnitId = (
  source: { ou_id?: string | null } | null | undefined,
  target: { ou_id?: string | null } | null | undefined,
): string | undefined => source?.ou_id || target?.ou_id || undefined
