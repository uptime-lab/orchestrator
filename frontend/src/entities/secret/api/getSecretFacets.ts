import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { SecretOUCountsResult, SecretTagListResult } from './types'

export const getSecretTags = async (): Promise<SecretTagListResult> => {
  const response = await apiClient.get<ApiResponse<SecretTagListResult>>(
    '/api/v1/secrets/tags',
  )
  return response.data.data
}

export const getSecretOUCounts = async (): Promise<SecretOUCountsResult> => {
  const response = await apiClient.get<ApiResponse<SecretOUCountsResult>>(
    '/api/v1/secrets/ou-counts',
  )
  return response.data.data
}
