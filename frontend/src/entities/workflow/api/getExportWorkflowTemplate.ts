import { apiClient } from '@/shared/utils/apiConfig'

// Fetches the portable playbook document for a template and triggers a
// browser download. The server strips account-specific ids, so the file is
// safe to share or move to another install.
const DEFAULT_FILENAME = 'playbook.playbook.json'

const parseFilename = (contentDisposition: unknown): string => {
  if (typeof contentDisposition !== 'string') {
    return DEFAULT_FILENAME
  }
  const match = contentDisposition.match(/filename="?([^"]+)"?/i)
  return match?.[1] ?? DEFAULT_FILENAME
}

export const downloadWorkflowTemplateExport = async (
  templateId: string,
): Promise<void> => {
  const response = await apiClient.get(
    `/api/v1/workflow-templates/${templateId}/export`,
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
