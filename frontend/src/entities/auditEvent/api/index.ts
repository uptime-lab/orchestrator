import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// The account's audit trail: who did what, when. Read-only by construction —
// nothing in the API can delete a row; only the retention sweep removes events
// older than the account's retention (the account record's audit_retention_days).
export type AuditEvent = {
  id: string
  // Absent for platform-initiated actions (the scheduler, the workflow engine,
  // the self-heal paths); actor_kind says which.
  actor_user_id?: string
  actor_kind: string
  action: string
  target_type: string
  target_id: string
  // Short note about WHICH part of the target changed. Never credential
  // material — the backend asserts that on the write side.
  detail?: string
  created_at: string
}

export type AuditEventListResult = {
  events: AuditEvent[]
  count: number
  total: number
  page: number
  per_page: number
}

export type AuditEventListParams = {
  page?: number
  per_page?: number
  // Exact action string, e.g. "secret.values_read".
  action?: string
  // Actor user id.
  actor?: string
}

export const getAuditEvents = async (
  filters: AuditEventListParams = {},
): Promise<AuditEventListResult> => {
  // Strings, and only the ones that are set: an empty `action=` is a filter
  // matching nothing, not an absent filter.
  const params: Record<string, string> = {}
  if (filters.page) params.page = String(filters.page)
  if (filters.per_page) params.per_page = String(filters.per_page)
  if (filters.action) params.action = filters.action
  if (filters.actor) params.actor = filters.actor
  const response = await apiClient.get<ApiResponse<AuditEventListResult>>(
    '/api/v1/audit-events',
    { params },
  )
  return response.data.data
}
