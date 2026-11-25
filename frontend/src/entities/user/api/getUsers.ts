import type { ApiResponse } from '@/entities/auth/api/postSignIn'
import { userMock } from '@/entities/user/mocks.ts'

import { apiClient } from '@/shared/utils/apiConfig'
import { getConfigValue } from '@/shared/utils/config.ts'

import { mapApiUserToEntity, type ApiUserResult } from './mapApiUserToEntity'
import type { UserEntity } from './types.ts'

type ApiUserListResult = {
  users: ApiUserResult[]
  count: number
}

export const getUsers = async (): Promise<UserEntity[]> => {
  if (getConfigValue('VITE_ESSENTIALS_API_ONLY') === 'on') {
    return [userMock]
  }

  const response =
    await apiClient.get<ApiResponse<ApiUserListResult>>('/api/v1/users')
  return response.data.data.users.map(mapApiUserToEntity)
}
