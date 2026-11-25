import { apiClient } from '@/shared/utils/apiConfig'

import { ApiResponse } from './postSignIn'

// A Session is one login, not one token: the refresh token rotates on every
// use, and every successor stays inside the same session. That is why the id
// here is stable enough to show a person and to revoke.
export type Session = {
  id: string
  user_agent?: string
  ip?: string
  created_at: string
  last_used_at: string
  expires_at: string
  // current marks the session this browser is signed in with.
  current: boolean
}

export const listSessions = async (): Promise<Session[]> => {
  const res = await apiClient.get<ApiResponse<Session[]>>('/auth/sessions')
  return res.data.data
}

export const revokeSession = async (id: string): Promise<void> => {
  await apiClient.delete(`/auth/sessions/${id}`)
}

// revokeOtherSessions signs out everywhere except here — deliberately keeping
// the caller signed in, because this is the "I lost a device" button and a user
// who gets logged out for pressing it will not press it.
export const revokeOtherSessions = async (): Promise<void> => {
  await apiClient.post('/auth/sessions/revoke-others')
}
