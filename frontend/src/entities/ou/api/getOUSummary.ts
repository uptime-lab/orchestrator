import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { OU, OUDeviceProfile } from './types'

// Where an effective policy value came from (node level never appears here —
// this is the UNIT's chain).
export type PolicySource = 'node' | 'ou' | 'root_ou' | 'account' | 'default'

export type ResolvedFlag = { value: boolean; source: PolicySource }
export type ResolvedString = {
  value: string
  present: boolean
  source: PolicySource
}
export type ResolvedUUID = {
  value: string | null
  present: boolean
  source: PolicySource
}

export type OUEffectivePolicy = {
  network_first: ResolvedFlag
  boot_to_os: ResolvedFlag
  ssh_password_auth: ResolvedFlag
  default_username: ResolvedString
  // Presence + source only; the value is never returned.
  default_password: ResolvedString
  ssh_key_id: ResolvedUUID
  os_image_id: ResolvedUUID
}

export type OUSummaryNode = {
  id: string
  name: string
  status: string
  via_child_ou?: string
  policy_mode: 'inherits' | 'overridden' | 'via_child'
  overridden_fields?: string[]
}

export type OUSummaryAccessEntry = {
  id: string
  type: 'user' | 'ca'
  username?: string
  key_count: number
  sudo: boolean
  effective_from: 'unit' | 'parent' | 'account'
  shadowed: boolean
}

export type OUSummarySecret = {
  id: string
  name: string
  kind: string
  sensitive: boolean
  source_level: 'ou' | 'root'
  via: 'direct' | 'tag'
  tag?: string
}

export type OUSummaryWorkflow = {
  id: string
  name: string
  status: string
  scope_ou_count: number
}

export type OUSummaryMasterToken = { id: string; name: string }

export type OUSummarySection<T> = { total: number; items: T[] }

export type OUSummary = {
  unit: OU
  effective_policy: OUEffectivePolicy
  effective_device_profile?: OUDeviceProfile
  effective_device_profile_source?: PolicySource
  effective_ssh_key_name?: string
  effective_os_image_name?: string
  counts: {
    children: number
    nodes: number
    child_nodes: number
    access_entries: number
    secrets: number
    workflows: number
    master_tokens: number
  }
  // How many nodes an ssh_password_auth flip on this unit would push
  // apply_access_config to.
  password_auth_impact: number
  nodes?: OUSummarySection<OUSummaryNode>
  access_entries?: OUSummarySection<OUSummaryAccessEntry>
  secrets?: OUSummarySection<OUSummarySecret>
  workflows?: OUSummarySection<OUSummaryWorkflow>
  master_tokens?: OUSummarySection<OUSummaryMasterToken>
  // Sections the caller's role could not read.
  omitted?: string[]
}

export const getOUSummary = async (ouId: string): Promise<OUSummary> => {
  const response = await apiClient.get<ApiResponse<OUSummary>>(
    `/api/v1/organizational-units/${ouId}/summary`,
  )
  return response.data.data
}
