import { apiClient } from '@/shared/utils/apiConfig'

export const getAdoptionFile = async (nodeId: string): Promise<Blob> => {
  const response = await apiClient.get(
    `/api/v1/nodes/${nodeId}/adoption-file`,
    {
      responseType: 'blob',
    },
  )
  return response.data
}

export const downloadAdoptionFile = async (
  nodeId: string,
  nodeName: string,
): Promise<void> => {
  const blob = await getAdoptionFile(nodeId)
  const url = window.URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `adoption-file-${nodeName || nodeId}.txt`
  document.body.appendChild(link)
  link.click()
  document.body.removeChild(link)
  window.URL.revokeObjectURL(url)
}
