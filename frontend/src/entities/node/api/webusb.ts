import type { ApiResponse } from '@/entities/auth/api/postSignIn'

import { apiClient } from '@/shared/utils/apiConfig'

export type WebUSBProvisionRequest = {
  soc: 'cm4' | 'cm5'
  serial: string
  node_name?: string
  default_username?: string
  default_password?: string
}

export type WebUSBProvisionResult = {
  node_id: string
  node_name: string
  package_base: string // e.g. /net_install/eeprom/by-serial/<serial>
  files: string[]
  // Short-lived HMAC capability the download endpoint requires, e.g.
  // "?exp=...&sig=...". Appended to each package-file request.
  sig_query?: string
}

// getBootcode fetches the generic per-SoC second-stage recovery.bin (for the
// WebUSB serial-harvest phase) as raw bytes. Same-origin, authenticated.
export const getBootcode = async (soc: 'cm4' | 'cm5'): Promise<ArrayBuffer> => {
  const res = await apiClient.get(`/api/v1/eeprom/bootcode/${soc}`, {
    responseType: 'arraybuffer',
  })
  return res.data as ArrayBuffer
}

// webusbProvision bakes a per-serial EEPROM tied to a created/matched node and
// returns where to fetch the signed package.
export const webusbProvision = async (
  body: WebUSBProvisionRequest,
): Promise<WebUSBProvisionResult> => {
  const res = await apiClient.post<ApiResponse<WebUSBProvisionResult>>(
    '/api/v1/eeprom/webusb-provision',
    body,
  )
  return res.data.data
}

// webusbCommitFlash finalizes a flash: the node was created in the "flashing"
// state at provision time (its adoption token is baked into the EEPROM before
// the write), and is revealed as ready_for_adoption only once the EEPROM write
// completed. Best-effort — an interrupted flash simply leaves the node showing
// "flashing" as a retry cue.
export const webusbCommitFlash = async (nodeId: string): Promise<void> => {
  await apiClient.post(`/api/v1/eeprom/webusb-provision/${nodeId}/committed`)
}

// fetchPackageFile fetches one file of a signed EEPROM package (same-origin
// /net_install/...) as raw bytes for pushing over WebUSB.
export const fetchPackageFile = async (
  packageBase: string,
  name: string,
  sigQuery = '',
): Promise<ArrayBuffer> => {
  const res = await apiClient.get(`${packageBase}/${name}${sigQuery}`, {
    responseType: 'arraybuffer',
    // /net_install is served by nginx, not the API; use an absolute path so the
    // axios baseURL ('/api') is not prepended.
    baseURL: '',
  })
  return res.data as ArrayBuffer
}
