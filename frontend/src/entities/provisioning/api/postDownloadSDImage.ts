import { apiClient } from '@/shared/utils/apiConfig'

import type { SDProvisioningOptions } from './types'

const DEFAULT_FILENAME = 'orchestrator-sd-provisioning.zip'

const parseFilename = (contentDisposition: unknown): string => {
  if (typeof contentDisposition !== 'string') {
    return DEFAULT_FILENAME
  }
  const match = contentDisposition.match(/filename="?([^"]+)"?/i)
  return match?.[1] ?? DEFAULT_FILENAME
}

export const downloadSDProvisioningImage = async (
  options: SDProvisioningOptions,
): Promise<void> => {
  // Include only the provided (non-empty) fields in the request body.
  const body: SDProvisioningOptions = {}
  if (options.name?.trim()) {
    body.name = options.name.trim()
  }
  if (options.default_username?.trim()) {
    body.default_username = options.default_username.trim()
  }
  if (options.default_password?.trim()) {
    body.default_password = options.default_password.trim()
  }
  if (options.ssh_key_id?.trim()) {
    body.ssh_key_id = options.ssh_key_id.trim()
  }
  if (options.expires_at?.trim()) {
    body.expires_at = options.expires_at.trim()
  }

  const response = await apiClient.post('/api/v1/provisioning/sd', body, {
    responseType: 'blob',
  })

  const filename = parseFilename(response.headers['content-disposition'])

  const blob = response.data as Blob
  const url = window.URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  window.URL.revokeObjectURL(url)
}
