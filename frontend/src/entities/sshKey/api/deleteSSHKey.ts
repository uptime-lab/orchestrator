import { apiClient } from '@/shared/utils/apiConfig'

export const deleteSSHKey = async (id: string): Promise<void> => {
  await apiClient.delete(`/api/v1/ssh-keys/${id}`)
}
