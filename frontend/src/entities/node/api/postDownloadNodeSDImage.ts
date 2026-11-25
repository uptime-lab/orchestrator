import { apiClient } from '@/shared/utils/apiConfig'

// downloadNodeSDImage fetches the per-node SD/USB provisioning bundle (a ZIP
// baked with THIS node's per-node token) and triggers a browser download.
const DEFAULT_FILENAME = 'orchestrator-node.zip'

const parseFilename = (contentDisposition: unknown): string => {
  if (typeof contentDisposition !== 'string') {
    return DEFAULT_FILENAME
  }
  const match = contentDisposition.match(/filename="?([^"]+)"?/i)
  return match?.[1] ?? DEFAULT_FILENAME
}

export const downloadNodeSDImage = async (nodeId: string): Promise<void> => {
  const response = await apiClient.post(
    `/api/v1/nodes/${nodeId}/provisioning/sd`,
    {},
    { responseType: 'blob' },
  )

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
