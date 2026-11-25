import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

export type SSHPreflightRequest = {
  host: string
  port?: number
  username: string
  password?: string
  ssh_key_id?: string
  use_agent?: boolean
  sudo_password?: string
  // The fingerprint the operator trusts (from an earlier probe). When set,
  // the probe connects strictly against it: a different key comes back as
  // host_key_changed with both fingerprints, and the handshake stops before
  // any credential is sent. Omitted only before first contact.
  expected_host_key_fingerprint?: string
  // With expected_host_key_fingerprint: let THIS probe accept a key that
  // differs from it. The operator has seen the host_key_changed report and
  // says the device was reinstalled. The server audits the replacement with
  // both fingerprints.
  accept_new_host_key?: boolean
}

export type SSHPreflightResult = {
  reachable: boolean
  error?: string
  host_key_fingerprint: string
  // Set when expected_host_key_fingerprint was sent and the device presented
  // a different key; host_key_fingerprint is then the NEW key and
  // expected_host_key_fingerprint the one that was trusted. Nothing else was
  // probed and no ticket is issued.
  host_key_changed?: boolean
  expected_host_key_fingerprint?: string
  // The probe carried no expectation and took the key on first use.
  first_contact?: boolean
  // accept_new_host_key was sent and the device did present a different key;
  // expected_host_key_fingerprint is the key that was replaced.
  host_key_reset?: boolean
  // The server's proof that this operator saw host_key_fingerprint on this
  // host:port just now. The adopt call presents it as host_key_ticket. Only
  // on a reachable probe; valid until host_key_ticket_expires_at (about 15
  // minutes) and also invalidated by a backend restart.
  host_key_ticket?: string
  host_key_ticket_expires_at?: string
  user: string
  arch: string
  arch_supported: boolean
  sudo_mode: string
  cdn_reachable: boolean
  cdn_checked: boolean
  notes?: string
  // Set when a node with this device's hardware serial already exists (an
  // accidental re-adopt); adoption reuses it instead of creating a duplicate.
  existing_node_id?: string
  existing_node_name?: string
  existing_node_status?: string
}

// sshPreflight runs a non-destructive SSH connectivity/capability probe against a
// device before adoption.
export const sshPreflight = async (
  body: SSHPreflightRequest,
): Promise<SSHPreflightResult> => {
  const response = await apiClient.post<ApiResponse<SSHPreflightResult>>(
    '/api/v1/nodes/ssh-preflight',
    body,
  )
  return response.data.data
}
