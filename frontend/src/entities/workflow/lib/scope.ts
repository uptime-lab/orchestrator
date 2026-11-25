import type { OU } from '@/entities/ou'

import type { WorkflowTemplate } from '../api/types'

// scopeOUIDs is the playbook's unit set, tolerating a server that still sends
// only the legacy single field.
export const scopeOUIDs = (template: WorkflowTemplate): string[] => {
  if (template.ou_ids && template.ou_ids.length > 0) return template.ou_ids
  return template.ou_id ? [template.ou_id] : []
}

// A workflow is available to an OU when it is global (no scope) or scoped to
// the OU itself or one of its ancestors. A node without an OU sees only
// global workflows.
//
// With a SET the rule is a union — the playbook is available wherever ANY of
// its units puts it — which mirrors what the server allows it to target.
export const isWorkflowAvailableForOU = (
  template: WorkflowTemplate,
  ouId: string | null | undefined,
  ous: OU[],
): boolean => {
  const scope = scopeOUIDs(template)
  if (scope.length === 0) return true
  if (!ouId) return false
  if (scope.includes(ouId)) return true
  const ou = ous.find((o) => o.id === ouId)
  return !!ou?.parent_id && scope.includes(ou.parent_id)
}

// Drafts are hidden from run pickers; templates without a status (legacy
// rows) count as active.
export const isWorkflowActive = (template: WorkflowTemplate): boolean =>
  (template.status ?? 'active') === 'active'
