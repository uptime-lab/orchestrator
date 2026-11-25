import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

import type { MasterTokenResult, UpdateMasterTokenRequest } from './types'

export const putUpdateMasterToken = async (
  tokenId: string,
  data: UpdateMasterTokenRequest,
): Promise<MasterTokenResult> => {
  const response = await apiClient.put<ApiResponse<MasterTokenResult>>(
    `/api/v1/master-tokens/${tokenId}`,
    data,
  )
  return response.data.data
}
