import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type {
  AccessEntry,
  AccessOUCountsResult,
  CreateAccessEntryRequest,
  EffectiveAccessEntry,
  UpdateAccessEntryRequest,
} from './types'

// Scope selector for the list endpoint. Exactly one of these reaches the API.
// 'unassigned' lists entries not applied to any node (the default bucket for a
// newly created entry); 'account' lists the fleet-wide defaults.
// Node-scoped access is fetched via getEffectiveAccess(nodeId), not this list
// selector, so there is no 'node' member here.
export type AccessScopeSelector =
  | { kind: 'account' }
  | { kind: 'unassigned' }
  | { kind: 'all' }
  | { kind: 'ou'; ouId: string }

const scopeParams = (scope: AccessScopeSelector): Record<string, string> => {
  switch (scope.kind) {
    case 'ou':
      return { ou_id: scope.ouId }
    case 'unassigned':
      return { scope: 'unassigned' }
    case 'all':
      return { scope: 'all' }
    default:
      return { scope: 'account' }
  }
}

export const getAccessEntries = async (
  scope: AccessScopeSelector,
): Promise<AccessEntry[]> => {
  const response = await apiClient.get<ApiResponse<AccessEntry[]>>(
    '/api/v1/access-entries',
    { params: scopeParams(scope) },
  )
  return response.data.data ?? []
}

export const getAccessEntry = async (entryId: string): Promise<AccessEntry> => {
  const response = await apiClient.get<ApiResponse<AccessEntry>>(
    `/api/v1/access-entries/${entryId}`,
  )
  return response.data.data
}

export const createAccessEntry = async (
  payload: CreateAccessEntryRequest,
): Promise<AccessEntry> => {
  const response = await apiClient.post<ApiResponse<AccessEntry>>(
    '/api/v1/access-entries',
    payload,
  )
  return response.data.data
}

export const updateAccessEntry = async (
  entryId: string,
  payload: UpdateAccessEntryRequest,
): Promise<AccessEntry> => {
  const response = await apiClient.put<ApiResponse<AccessEntry>>(
    `/api/v1/access-entries/${entryId}`,
    payload,
  )
  return response.data.data
}

export const deleteAccessEntry = async (entryId: string): Promise<void> => {
  await apiClient.delete(`/api/v1/access-entries/${entryId}`)
}

// Attach/detach are per key rather than a whole-list PUT on purpose: replacing
// the set would let a stale client silently wipe keys it never saw — the same
// class of bug as the drag-to-replace this feature removes.
export const attachAccessKey = async (
  entryId: string,
  keyId: string,
): Promise<void> => {
  await apiClient.post(`/api/v1/access-entries/${entryId}/keys/${keyId}`)
}

export const detachAccessKey = async (
  entryId: string,
  keyId: string,
): Promise<void> => {
  await apiClient.delete(`/api/v1/access-entries/${entryId}/keys/${keyId}`)
}

export const getAccessOUCounts = async (): Promise<AccessOUCountsResult> => {
  const response = await apiClient.get<ApiResponse<AccessOUCountsResult>>(
    '/api/v1/access-entries/ou-counts',
  )
  return (
    response.data.data ?? {
      ous: [],
      unassigned: { users: 0, cas: 0 },
      account_wide: { users: 0, cas: 0 },
      total: 0,
    }
  )
}

export const getEffectiveAccess = async (
  nodeId: string,
): Promise<EffectiveAccessEntry[]> => {
  const response = await apiClient.get<ApiResponse<EffectiveAccessEntry[]>>(
    `/api/v1/nodes/${nodeId}/effective-access`,
  )
  return response.data.data ?? []
}

// Manual re-push of the node's effective access config to the device — the
// operator companion of the automatic re-apply after recovery. queued=true
// means the node is offline or in recovery and the command waits.
export const reapplyNodeAccess = async (
  nodeId: string,
): Promise<{ queued: boolean }> => {
  const response = await apiClient.post<ApiResponse<{ queued: boolean }>>(
    `/api/v1/nodes/${nodeId}/access/re-apply`,
  )
  return response.data.data ?? { queued: false }
}

// Recovery-mode apply: while the node is netbooted, the agent
// mounts the target disk and writes the resolved access config into the
// installed OS. Settings take effect on the next boot from that disk.
export const applyOfflineNodeAccess = async (
  nodeId: string,
  request: { target_device: string; root_partition?: number },
): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/access/apply-offline`, {
    target_device: request.target_device,
    root_partition: request.root_partition ?? 0,
  })
}

export const apiAccess = {
  applyOfflineNodeAccess,
  getAccessEntries,
  getAccessEntry,
  createAccessEntry,
  updateAccessEntry,
  deleteAccessEntry,
  attachAccessKey,
  detachAccessKey,
  getEffectiveAccess,
  getAccessOUCounts,
  reapplyNodeAccess,
}

export * from './types'
