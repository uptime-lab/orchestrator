import { apiClient } from '@/shared/utils/apiConfig'

export const deleteMasterToken = async (tokenId: string): Promise<void> => {
  await apiClient.delete(`/api/v1/master-tokens/${tokenId}`)
}
