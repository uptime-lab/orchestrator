import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

/**
 * A device this account's install flow turned away because its hardware serial
 * is registered to another account.
 *
 * The device itself only ever receives a deliberately vague refusal and keeps
 * retrying, so this record is how the person waiting on Add Node learns why
 * nothing appears. Derived by the backend from its own refusal record at read
 * time; nothing here is stored for the UI's sake.
 *
 * It names a SERIAL binding, never the owning account and never a device
 * movement: a serial can be bound by an account that never held the hardware.
 */
export interface AdoptionRefusal {
  hardware_serial: string
  first_seen_at: string
  last_seen_at: string
  occurrences: number
  /** Refused within the last few minutes: powered on and still retrying. */
  live: boolean
  /**
   * The serial is held by another account right now. False means the other
   * side has released it and the device will be admitted on its next retry.
   */
  still_bound_elsewhere: boolean
}

interface AdoptionRefusalsResponse {
  adoption_refusals: AdoptionRefusal[] | null
  live_window_seconds: number
}

export const getAdoptionRefusals = async (): Promise<AdoptionRefusal[]> => {
  // Two levels: every handler answers through the `{success, data}` envelope,
  // so the payload is at `data.data` — the trap getDeletedNodes documents.
  const { data } = await apiClient.get<ApiResponse<AdoptionRefusalsResponse>>(
    '/api/v1/nodes/adoption-refusals',
  )
  return data.data.adoption_refusals ?? []
}
