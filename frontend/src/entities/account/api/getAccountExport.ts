import { apiClient } from '@/shared/utils/apiConfig'

// Downloads the whole-account data export as a file.
//
// Unlike the image download this DOES pull the bytes through axios, and that is
// the right call here for the opposite reason: the document is a JSON object of
// kilobytes, not an OS image of gigabytes, and it is served by an endpoint that
// needs the Authorization header. Turning the response into a blob URL is what
// lets the browser save it without a second, unauthenticated route existing.
export const getAccountExport = async (): Promise<{
  blobUrl: string
  fileName: string
  revoke: () => void
}> => {
  const response = await apiClient.get('/api/v1/account/export', {
    responseType: 'blob',
  })

  // Prefer the server's own Content-Disposition name: it carries the account id
  // and the date, which is what lets someone tell two exports apart.
  const disposition = String(response.headers?.['content-disposition'] ?? '')
  const match = disposition.match(/filename="([^"]+)"/)
  const fileName = match?.[1] ?? 'orchestrator-export.json'

  const blobUrl = URL.createObjectURL(
    new Blob([response.data], { type: 'application/json' }),
  )
  return { blobUrl, fileName, revoke: () => URL.revokeObjectURL(blobUrl) }
}
