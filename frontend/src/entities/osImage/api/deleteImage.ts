import { apiClient } from '@/shared/utils/apiConfig'

export const deleteImage = async (id: string): Promise<void> => {
  await apiClient.delete(`/api/v1/images/${id}`)
}
