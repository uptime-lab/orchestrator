import { publicApiClient } from '@/shared/utils/apiConfig'

import { type ApiResponse } from './postSignIn'

export type RefreshTokenPayload = {
  refresh_token: string
}

export type TokenRefreshResult = {
  access_token: string
  refresh_token: string
  expires_in: number
  token_type: string
}

export const postRefreshToken = async (
  payload: RefreshTokenPayload,
): Promise<TokenRefreshResult> => {
  const response = await publicApiClient.post<ApiResponse<TokenRefreshResult>>(
    '/auth/refresh',
    payload,
  )
  return response.data.data
}
