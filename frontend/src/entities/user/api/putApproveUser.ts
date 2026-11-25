import { apiClient } from '@/shared/utils/apiConfig'

export const putApproveUser = async ({
  user_id,
}: {
  user_id: string
}): Promise<'ok'> => {
  await apiClient.put(`/user/${user_id}/approve`)
  return 'ok'
}
