import type { ApiResponse } from '@/entities/auth/api/postSignIn'
import type { LEDConfig } from '@/entities/node/api/led'
import type { WatchdogConfig } from '@/entities/node/api/watchdog'

import { apiClient } from '@/shared/utils/apiConfig'

import type { Flag, OUDeviceProfile } from './types'

// Account-wide policy fallback: applies to unassigned nodes and any field the
// OU chain leaves unresolved.
export type AccountPolicy = {
  account_id: string
  network_first: Flag
  boot_to_os: Flag
  ssh_password_auth: Flag
  default_username?: string
  has_default_password: boolean
  ssh_key_id?: string
  os_image_id?: string
  // Account-level hardware equipage (omitted = none). Same one-block shape as
  // the unit's.
  device_profile?: OUDeviceProfile
  // Account-level LED fallback (omitted = none).
  led_config?: LEDConfig
  // Account-level watchdog fallback (omitted = none). Declared state only; a
  // device receives it through its per-node apply.
  watchdog_config?: WatchdogConfig
  updated_at: string
}

export type UpdateAccountPolicyRequest = {
  network_first?: Flag
  boot_to_os?: Flag
  ssh_password_auth?: Flag
  // Provisioning defaults: omitted = unchanged, "" = clear to unset, value = set
  default_username?: string
  default_password?: string
  ssh_key_id?: string
  os_image_id?: string
  // Omitted = unchanged; empty profile_id clears back to none.
  device_profile?: OUDeviceProfile
  // Omitted = unchanged; all-defaults clears back to none.
  led_config?: LEDConfig
  // Omitted = unchanged; a disabled config clears back to none.
  watchdog_config?: WatchdogConfig
}

export const getAccountPolicy = async (): Promise<AccountPolicy> => {
  const response = await apiClient.get<ApiResponse<AccountPolicy>>(
    '/api/v1/account-policy',
  )
  return response.data.data
}

export const putAccountPolicy = async (
  data: UpdateAccountPolicyRequest,
): Promise<AccountPolicy> => {
  const response = await apiClient.put<ApiResponse<AccountPolicy>>(
    '/api/v1/account-policy',
    data,
  )
  return response.data.data
}
