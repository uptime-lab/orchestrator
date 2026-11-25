import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { TrackedFile, TrackedFileRead } from './types.ts'

// Tracked files: the platform follows a file on a node, keeps versioned copies,
// and can write it back or remove it. Reads are asynchronous — the backend
// dispatches an agent command and the caller polls it, the same shape the
// OS-user and EEPROM scans use.

export const listTrackedFiles = async (
  nodeId: string,
): Promise<TrackedFile[]> => {
  const response = await apiClient.get<ApiResponse<{ files: TrackedFile[] }>>(
    `/api/v1/nodes/${nodeId}/files`,
  )
  return response.data.data.files || []
}

// trackFile starts following a new path; refreshFile re-reads one already
// tracked. Both return a command id to poll with getFileReadResult.
export const trackFile = async (
  nodeId: string,
  path: string,
): Promise<string> => {
  const response = await apiClient.post<ApiResponse<{ command_id: string }>>(
    `/api/v1/nodes/${nodeId}/files/track`,
    { path },
  )
  return response.data.data.command_id
}

export const refreshFile = async (
  nodeId: string,
  path: string,
): Promise<string> => {
  const response = await apiClient.post<ApiResponse<{ command_id: string }>>(
    `/api/v1/nodes/${nodeId}/files/refresh`,
    { path },
  )
  return response.data.data.command_id
}

export const getFileReadResult = async (
  nodeId: string,
  commandId: string,
  path: string,
): Promise<TrackedFileRead> => {
  const response = await apiClient.get<ApiResponse<TrackedFileRead>>(
    `/api/v1/nodes/${nodeId}/files/read/${commandId}`,
    { params: { path } },
  )
  return response.data.data
}

// untrackFile removes the file and its history from the platform. The file on
// the device is left alone.
export const untrackFile = async (
  nodeId: string,
  path: string,
): Promise<void> => {
  await apiClient.post(`/api/v1/nodes/${nodeId}/files/untrack`, { path })
}

// deleteFileOnDevice removes the file from the node itself. confirmPath is
// checked server-side, so the typed confirmation is not a browser-only control.
export const deleteFileOnDevice = async (
  nodeId: string,
  path: string,
  confirmPath: string,
  backup: boolean,
): Promise<string> => {
  const response = await apiClient.post<ApiResponse<{ command_id: string }>>(
    `/api/v1/nodes/${nodeId}/files/delete-on-device`,
    { path, confirm_path: confirmPath, backup },
  )
  return response.data.data.command_id
}
