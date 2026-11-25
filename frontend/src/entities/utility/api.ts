import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// Utilities: OU-assignable software roles (Docker being the first). The
// catalog is a typed registry served by the backend; an operator creates
// ASSIGNMENTS — one utility × one target × one config — where the target is
// account-wide XOR one OU XOR one node XOR unassigned. Removing an assignment
// only stops managing the software (retain semantics); Apply is explicit.
// Plain api functions per the entities convention; useQuery wrappers live in
// hooks.ts.

export type UtilityClass = 'reconciled'

// Same JSON shape as the workflow block registry's params, plus the numeric
// bounds — the catalog drives the generated config forms.
export type UtilityParam = {
  name: string
  label: string
  description?: string
  details?: string
  type: string
  required: boolean
  default?: unknown
  enum_values?: string[]
  placeholder?: string
  min?: number
  max?: number
}

export type UtilityDefinition = {
  id: string
  name: string
  description: string
  details?: string
  class: UtilityClass
  depends_on?: string[]
  conflicts_with?: string[]
  requires_reboot: boolean
  min_agent_version?: string
  params: UtilityParam[]
}

export type UtilityScope = 'account' | 'ou' | 'node' | 'unassigned'

export type UtilityAssignment = {
  id: string
  utility_id: string
  scope: UtilityScope
  // The OU SET the assignment covers. `ou_id` is the legacy first unit and is
  // still sent by the server; read the set through assignmentOUIDs so a server
  // that predates the array keeps working.
  ou_ids?: string[]
  ou_id?: string
  node_id?: string
  account_wide: boolean
  config?: Record<string, unknown>
  enabled: boolean
  created_at: string | number
  updated_at: string | number
}

// The assignment's unit set, tolerating a server that still sends only the
// legacy single field (the workflow scopeOUIDs shape).
export const assignmentOUIDs = (assignment: UtilityAssignment): string[] => {
  if (assignment.ou_ids && assignment.ou_ids.length > 0)
    return assignment.ou_ids
  return assignment.ou_id ? [assignment.ou_id] : []
}

// Per-node outcome of an explicit Apply. queued = the command registered for
// an offline node and delivers on its next managed-OS connect; skipped = a
// deliberate refusal, with the platform's reason verbatim.
export type UtilityApplyResult = {
  node_id: string
  node_name: string
  command_id?: string
  queued: boolean
  skipped: boolean
  reason?: string
}

export type EffectiveUtilityStatus =
  'declared' | 'update_pending' | 'applied' | 'failed'

export type EffectiveUtility = {
  utility_id: string
  name: string
  class: UtilityClass
  assignment_id: string
  source: 'node' | 'ou' | 'account'
  source_ou_id?: string
  // Set when the utility is in the set only because another one depends on
  // it (docker under kiosk); such an entry has no assignment of its own.
  implied_by?: string[]
  config?: Record<string, unknown>
  desired_hash: string
  applied_hash?: string
  status: EffectiveUtilityStatus
  last_error?: string
  details?: string
}

export type CreateUtilityAssignmentPayload = {
  utility_id: string
  account_wide?: boolean
  // The unit SET; the legacy single ou_id is still accepted and merged
  // server-side.
  ou_ids?: string[]
  ou_id?: string
  node_id?: string
  config?: Record<string, unknown>
  enabled?: boolean
}

// The scope is replaced only when retarget=true — then {account_wide, ou_ids,
// node_id} REPLACE the whole target; without it the target fields are ignored
// and only config/enabled change.
export type UpdateUtilityAssignmentPayload = {
  config?: Record<string, unknown>
  enabled?: boolean
  retarget?: boolean
  account_wide?: boolean
  ou_ids?: string[]
  ou_id?: string
  node_id?: string
}

// The 409 code for an existing (utility, target) pair — at most one assignment
// per pair by construction, so a duplicate create is an expected refusal, not
// an error to alarm about.
export const UTILITY_ASSIGNMENT_DUPLICATE_CODE = 'utility_assignment_duplicate'

// True when an error is the duplicate-pair 409 above.
export const isDuplicateAssignmentError = (err: unknown): boolean => {
  if (typeof err !== 'object' || err === null) return false
  const resp = (
    err as {
      response?: { status?: number; data?: { code?: string; error?: string } }
    }
  ).response
  if (!resp) return false
  return (
    resp.data?.code === UTILITY_ASSIGNMENT_DUPLICATE_CODE ||
    resp.data?.error === UTILITY_ASSIGNMENT_DUPLICATE_CODE
  )
}

// True for any assignment-scope 409 — no two assignments of one utility may
// share an OU, and the server's message names the conflicting assignment, so
// the caller shows it verbatim and must NOT retry.
export const isAssignmentConflictError = (err: unknown): boolean => {
  if (typeof err !== 'object' || err === null) return false
  const resp = (err as { response?: { status?: number } }).response
  return resp?.status === 409
}

export const getUtilityCatalog = async (): Promise<UtilityDefinition[]> => {
  const response = await apiClient.get<ApiResponse<UtilityDefinition[]>>(
    '/api/v1/utilities/catalog',
  )
  return response.data.data
}

export const getUtilityAssignments = async (): Promise<UtilityAssignment[]> => {
  const response =
    await apiClient.get<ApiResponse<UtilityAssignment[]>>('/api/v1/utilities')
  return response.data.data
}

export const getUtilityOUCounts = async (): Promise<Record<string, number>> => {
  const response = await apiClient.get<ApiResponse<Record<string, number>>>(
    '/api/v1/utilities/ou-counts',
  )
  return response.data.data
}

export const createUtilityAssignment = async (
  payload: CreateUtilityAssignmentPayload,
): Promise<UtilityAssignment> => {
  const response = await apiClient.post<ApiResponse<UtilityAssignment>>(
    '/api/v1/utilities/assignments',
    payload,
  )
  return response.data.data
}

export const getUtilityAssignment = async (
  assignmentId: string,
): Promise<UtilityAssignment> => {
  const response = await apiClient.get<ApiResponse<UtilityAssignment>>(
    `/api/v1/utilities/assignments/${assignmentId}`,
  )
  return response.data.data
}

export const updateUtilityAssignment = async (
  assignmentId: string,
  payload: UpdateUtilityAssignmentPayload,
): Promise<UtilityAssignment> => {
  const response = await apiClient.put<ApiResponse<UtilityAssignment>>(
    `/api/v1/utilities/assignments/${assignmentId}`,
    payload,
  )
  return response.data.data
}

// 204 — retain semantics: software already installed stays on the devices.
export const deleteUtilityAssignment = async (
  assignmentId: string,
): Promise<void> => {
  await apiClient.delete(`/api/v1/utilities/assignments/${assignmentId}`)
}

export const applyUtilityAssignment = async (
  assignmentId: string,
): Promise<UtilityApplyResult[]> => {
  const response = await apiClient.post<ApiResponse<UtilityApplyResult[]>>(
    `/api/v1/utilities/assignments/${assignmentId}/apply`,
  )
  return response.data.data
}

export const getNodeEffectiveUtilities = async (
  nodeId: string,
): Promise<EffectiveUtility[]> => {
  const response = await apiClient.get<ApiResponse<EffectiveUtility[]>>(
    `/api/v1/nodes/${nodeId}/effective-utilities`,
  )
  return response.data.data
}

// One utility's rollup across a unit's nodes.
//
// `nodes` is how many of the unit's nodes the utility RESOLVES to, which is not
// the unit's node count: a nearer assignment on a node overrides the unit's, and
// a child unit can resolve a different set. The four rungs sum to it.
export type OUUtilityLadder = {
  utility_id: string
  name: string
  nodes: number
  applied: number
  update_pending: number
  failed: number
  declared: number
}

export const getOUUtilityLadder = async (
  ouId: string,
): Promise<OUUtilityLadder[]> => {
  const response = await apiClient.get<ApiResponse<OUUtilityLadder[]>>(
    `/api/v1/organizational-units/${ouId}/utility-ladder`,
  )
  return response.data.data
}

export const applyNodeUtilities = async (
  nodeId: string,
): Promise<UtilityApplyResult> => {
  const response = await apiClient.post<ApiResponse<UtilityApplyResult>>(
    `/api/v1/nodes/${nodeId}/utilities/apply`,
  )
  return response.data.data
}
