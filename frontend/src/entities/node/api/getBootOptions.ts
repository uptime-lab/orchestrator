import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

export type BootOption = {
  value: string
  label: string
  hex_digit: string
  is_default: boolean
  description: string
}

export type BootOptionsResult = {
  options: BootOption[]
}

export const getBootOptions = async (): Promise<BootOption[]> => {
  const response = await apiClient.get<ApiResponse<BootOptionsResult>>(
    '/api/v1/boot-options',
  )
  return response.data.data.options
}
