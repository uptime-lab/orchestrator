import { apiClient } from '@/shared/utils/apiConfig'

import type { UpdateSecretTagRequest, UpsertSecretTagRequest } from './types'

// Create a tag with a name + palette color (or recolor an existing one).
export const createSecretTag = async (
  data: UpsertSecretTagRequest,
): Promise<void> => {
  await apiClient.post('/api/v1/secret-tags', data)
}

// Rename (cascades across secrets + attachments) and/or recolor a tag.
export const updateSecretTag = async (
  tag: string,
  data: UpdateSecretTagRequest,
): Promise<void> => {
  await apiClient.put(`/api/v1/secret-tags/${encodeURIComponent(tag)}`, data)
}

// Delete a tag entirely (strip it from every secret + drop its attachments).
export const deleteSecretTag = async (tag: string): Promise<void> => {
  await apiClient.delete(`/api/v1/secret-tags/${encodeURIComponent(tag)}`)
}
