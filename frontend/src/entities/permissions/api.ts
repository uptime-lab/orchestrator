import { apiClient } from '@/shared/utils/apiConfig'

// PermissionMap maps an action to the scope where the user holds it: "*" for
// account-wide, otherwise a list of OU ids. Actions absent from the map are
// not permitted anywhere.
export type PermissionMap = Record<string, '*' | string[]>

type ApiResponse<T> = {
  success: boolean
  message: string
  data: T
  timestamp: string
}

export const getMyPermissions = async (): Promise<PermissionMap> => {
  const res = await apiClient.get<ApiResponse<PermissionMap>>(
    '/api/v1/users/me/permissions',
  )
  return res.data.data ?? {}
}

// RolePermission is one tunable action's state for a role: its built-in
// default, effective value, and whether an override is in effect.
export type RolePermission = {
  action: string
  default: boolean
  allowed: boolean
  overridden: boolean
}

export type RolePermissions = {
  role: string
  permissions: RolePermission[]
}

export const getRolePermissions = async (): Promise<RolePermissions[]> => {
  const res = await apiClient.get<ApiResponse<RolePermissions[]>>(
    '/api/v1/account-role-settings',
  )
  return res.data.data ?? []
}

export const putRoleSetting = async (
  role: string,
  action: string,
  allowed: boolean,
): Promise<void> => {
  await apiClient.put('/api/v1/account-role-settings', {
    role,
    action,
    allowed,
  })
}

// resetRoleSetting reverts a single action (when action given) or the whole
// role (when omitted) to the built-in defaults.
export const resetRoleSetting = async (
  role: string,
  action?: string,
): Promise<void> => {
  const params = new URLSearchParams({ role })
  if (action) params.set('action', action)
  await apiClient.delete(`/api/v1/account-role-settings?${params.toString()}`)
}
