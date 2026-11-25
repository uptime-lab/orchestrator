import { apiClient } from '@/shared/utils/apiConfig'

// downloadSSHKeyPublicKey fetches a key's public part (authorized_keys line) and
// triggers a browser download.
export const downloadSSHKeyPublicKey = async (keyId: string): Promise<void> => {
  const response = await apiClient.get(`/api/v1/ssh-keys/${keyId}/public-key`, {
    responseType: 'blob',
  })
  const blob = response.data as Blob
  const url = window.URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `ssh-key-${keyId}.pub`
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  window.URL.revokeObjectURL(url)
}

// getSSHKeyPublicKeyText fetches the public key as text (for inline display/copy).
export const getSSHKeyPublicKeyText = async (
  keyId: string,
): Promise<string> => {
  const response = await apiClient.get<string>(
    `/api/v1/ssh-keys/${keyId}/public-key`,
    { responseType: 'text' },
  )
  return response.data
}
