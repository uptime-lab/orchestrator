import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import { mapApiUserToEntity, type ApiUserResult } from './mapApiUserToEntity'
import type { UserEntity } from './types'

// putMyName changes the signed-in user's display name. The update is
// partial: only the name is sent, so the email (the sign-in identifier) is
// untouched. The route asks for a fresh identity confirmation, which the
// API client prompts for.
export const putMyName = async (name: string): Promise<UserEntity> => {
  const response = await apiClient.put<ApiResponse<ApiUserResult>>(
    '/api/v1/users/me',
    { name },
  )
  return mapApiUserToEntity(response.data.data)
}
