import { apiClient } from '@/shared/utils/apiConfig'

export const deleteOU = async (ouId: string): Promise<void> => {
  await apiClient.delete(`/api/v1/organizational-units/${ouId}`)
}
