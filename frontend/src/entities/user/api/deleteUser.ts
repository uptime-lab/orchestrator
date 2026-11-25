import { apiClient } from '@/shared/utils/apiConfig'

export const deleteUser = async (payload: {
  user_id: string
}): Promise<'ok'> => {
  await apiClient.delete(`/user/${payload.user_id}`)
  return 'ok'
}
