import { apiClient } from '@/shared/utils/apiConfig'

export const deleteSecret = async (secretId: string): Promise<void> => {
  await apiClient.delete(`/api/v1/secrets/${secretId}`)
}
