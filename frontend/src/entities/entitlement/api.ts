import { apiClient } from '@/shared/utils/apiConfig'

// Entitlement mirrors GET /api/v1/server/entitlement: what this install runs
// under and what that means for the next device. Instance-wide; the backend
// serves it only to entitlement.manage holders.
export type EntitlementState =
  'free' | 'active' | 'grace' | 'lapsed' | 'rejected'

export type EntitlementSource = 'none' | 'file' | 'database'

export type EntitlementRejection = {
  source: EntitlementSource
  reason: string
}

// Two shapes: the account that operates the install gets the full card and
// may apply a file; any other account's super_admin gets the summary (state,
// caps, the account's own count, expiry, grace) and none of the terms, nor
// the install-wide count.
export type Entitlement = {
  instance_operator: boolean
  state: EntitlementState
  source?: EntitlementSource
  free_device_cap: number
  // 0 = unlimited.
  effective_device_cap: number
  // The install-wide admitted count, every account counted. Operator only;
  // -1 when the count could not be read.
  admitted_devices?: number
  // The caller's own account's admitted count; -1 when it could not be
  // read. Absent from a backend that predates it.
  account_devices?: number
  subscription_url: string
  customer_id?: string
  kind?: 'evaluation' | 'subscription' | 'hosted'
  device_cap?: number
  // Operator only: device_cap is positive but below free_device_cap, so the
  // free cap is what effective_device_cap carries.
  device_cap_below_free_floor?: boolean
  issued_at?: string
  expires_at?: string
  grace_days?: number
  grace_until?: string
  note?: string
  applied_at?: string
  rejections?: EntitlementRejection[]
}

type ApiResponse<T> = {
  success: boolean
  message: string
  data: T
  timestamp: string
}

export type AccountPlan = 'free' | 'subscription'

// AccountLimitEntitlement is the third shape: the install sets a
// per-account device limit and the caller's account does not operate the
// install. It is the account's own limit and nothing of the install's
// entitlement, which is the operator's licence.
export type AccountLimitEntitlement = {
  instance_operator: false
  mode: 'account_limit'
  // The account's admitted count; -1 when it could not be read.
  account_devices: number
  // The account's limit now: 0 = no limit, -1 = it could not be read.
  account_device_limit: number
  // free = the install's default limit; subscription = a raised (or
  // otherwise set) limit for this account. Absent when unreadable.
  account_plan?: AccountPlan
  // When a dated subscription ends; absent otherwise.
  account_plan_until?: string
  // Where to write for more devices; absent when none is configured.
  account_limit_contact?: string
}

// EntitlementAnswer is what GET /api/v1/server/entitlement answers. An
// answer without a mode comes from a backend that predates it and is the
// instance shape.
export type EntitlementAnswer =
  (Entitlement & { mode?: 'instance' }) | AccountLimitEntitlement

export const isAccountLimitEntitlement = (
  e: EntitlementAnswer,
): e is AccountLimitEntitlement => e.mode === 'account_limit'

// canApplyEntitlement: the paste is the instance operator's alone, and an
// account-limit answer is never the operator's.
export const canApplyEntitlement = (e: EntitlementAnswer | null): boolean =>
  e !== null && !isAccountLimitEntitlement(e) && e.instance_operator

export const getEntitlement = async (): Promise<EntitlementAnswer> => {
  const res = await apiClient.get<ApiResponse<EntitlementAnswer>>(
    '/api/v1/server/entitlement',
  )
  return res.data.data
}

// AppliedEntitlement describes the file that was just pasted, which is not
// necessarily the one in force: the install runs under the newest issued file
// that has not lapsed, so an older or expired paste is stored and outranked.
// Absent from a backend that predates it.
export type AppliedEntitlement = {
  in_force: boolean
  issued_at: string
  expires_at: string
  device_cap: number
  state: 'active' | 'grace' | 'lapsed'
}

// The PUT answers with the entitlement now in force plus, optionally, what
// became of the pasted file.
export type ApplyEntitlementResult = Entitlement & {
  applied?: AppliedEntitlement
}

// The PUT sits behind the step-up; the API client replays it with the ticket
// after the confirmation dialog, so the caller sees one promise.
export const applyEntitlement = async (
  document: string,
): Promise<ApplyEntitlementResult> => {
  const res = await apiClient.put<ApiResponse<ApplyEntitlementResult>>(
    '/api/v1/server/entitlement',
    { document },
  )
  return res.data.data
}
