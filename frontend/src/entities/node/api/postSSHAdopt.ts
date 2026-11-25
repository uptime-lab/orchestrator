import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

export type SSHAdoptRequest = {
  host: string
  port?: number
  username: string
  password?: string
  auth_ssh_key_id?: string
  use_agent?: boolean
  sudo_password?: string
  // The adoption never accepts a host key on first use. Send exactly one of:
  // host_key_ticket, the proof a reachable preflight returned (bound to this
  // account, user, host and port, valid about 15 minutes) — what the UI
  // sends; or host_key_fingerprint, the device's SHA256 host key known out of
  // band. Adoption connects strictly against that key, so a device (or an
  // impostor) presenting a different one is refused before any credential is
  // offered. Neither, or a ticket that does not verify for this request, is a
  // 400 with code host_key_binding_required.
  host_key_ticket?: string
  host_key_fingerprint?: string
  node_name: string
  default_username: string
  default_password: string
  node_ssh_key_id?: string
  provision_network_first?: boolean
}

export type AdoptionJobStep = {
  key: string
  label: string
  status: string
  message?: string
}

export type AdoptionJob = {
  id: string
  account_id: string
  node_id?: string
  host: string
  username: string
  status: string
  steps: AdoptionJobStep[]
  host_key_fingerprint?: string
  error?: string
  created_at: string
  updated_at: string
}

// sshAdopt starts an asynchronous SSH device adoption and returns the job.
export const sshAdopt = async (body: SSHAdoptRequest): Promise<AdoptionJob> => {
  const response = await apiClient.post<ApiResponse<AdoptionJob>>(
    '/api/v1/nodes/ssh-adopt',
    body,
  )
  return response.data.data
}

// getAdoptionJob polls an adoption job's current state.
export const getAdoptionJob = async (jobId: string): Promise<AdoptionJob> => {
  const response = await apiClient.get<ApiResponse<AdoptionJob>>(
    `/api/v1/node-adoption-jobs/${jobId}`,
  )
  return response.data.data
}
