import { apiClient } from '@/shared/utils/apiConfig'

// putMyPassword sets (or changes) the current user's password. The backend
// requires no current password, so a passwordless OIDC user can add one and a
// password user can rotate theirs. All other sessions' refresh tokens are
// invalidated server-side.
export const putMyPassword = async (password: string): Promise<void> => {
  await apiClient.put('/api/v1/users/me/password', { password })
}
