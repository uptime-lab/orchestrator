import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

// The bootloader firmware base the EEPROM signer currently bakes from, per
// SoC family ("cm4"/"cm5"). A SoC may be absent; the object is empty when the
// signer is not configured.
export type BaseFirmware = {
  version: string // short build hash, e.g. "f68405bc"
  image: string // base image filename, e.g. "pieeprom-2026-06-29.bin"
  build_timestamp?: number // firmware build time (unix seconds)
}

export type BaseFirmwareMap = Partial<Record<'cm4' | 'cm5', BaseFirmware>>

export const getEEPROMBaseFirmware = async (): Promise<BaseFirmwareMap> => {
  const response = await apiClient.get<ApiResponse<BaseFirmwareMap>>(
    '/api/v1/eeprom/base-firmware',
  )
  return response.data.data
}
