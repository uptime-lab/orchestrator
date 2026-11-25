import { publicApiClient } from '@/shared/utils/apiConfig'

import { ApiResponse } from './postSignIn'

// postLogout revokes the session server-side.
//
// Clearing localStorage alone only forgets the tokens locally; the refresh
// token stayed valid for its full TTL, so anything that had already copied it
// could keep minting access tokens. This ends the session at the source.
//
// Uses the PUBLIC client on purpose: logout must work even when the access
// token has already expired, and it must never trigger the 401 refresh
// interceptor — refreshing a session we are in the middle of destroying would
// resurrect it.
export const postLogout = async (refreshToken: string): Promise<void> => {
  await publicApiClient.post<ApiResponse<{ revoked: boolean }>>(
    '/auth/logout',
    { refresh_token: refreshToken },
  )
}
