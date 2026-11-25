import { apiClient } from '@/shared/utils/apiConfig'

// ServerCapabilities mirrors GET /api/v1/server/capabilities: which optional
// feature groups this deployment supports. An install whose devices sit behind
// customer NAT lacks the features that need the server on the devices'
// network; read each flag rather than inferring it from the install type.
export type ServerCapabilities = {
  ssh_adopt: boolean
  eeprom_signer: boolean
  netboot: boolean
  sd_provisioning: boolean
  terminal: boolean
  one_command_adopt: boolean
  // The netboot HTTP_HOST this install serves from; used to flag a device whose
  // EEPROM points at a different install. Empty when netboot is not configured.
  boot_host?: string
  // Disk quotas; absent/0 = unlimited.
  max_image_upload_size?: number
  max_images_per_account?: number
  // An account-deletion request is carried out by the install's operator
  // console (the hosted install). False on a self-hosted install, which only
  // records the request. Absent until loaded or from an older backend.
  account_erasure?: boolean
  // Days a device bound to a never-admitted node may stay silent before its
  // serial is released; 0 = never. Absent from an older backend.
  serial_lease_days?: number
  // Commit the backend was built from; absent when the build recorded none.
  revision?: string
  // Platform release the backend was built for; empty for a build from
  // source, absent from an older backend.
  version?: string
}

type ApiResponse<T> = {
  success: boolean
  message: string
  data: T
  timestamp: string
}

export const getServerCapabilities = async (): Promise<ServerCapabilities> => {
  const res = await apiClient.get<ApiResponse<ServerCapabilities>>(
    '/api/v1/server/capabilities',
  )
  return res.data.data
}
