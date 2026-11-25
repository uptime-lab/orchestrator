import { t } from 'i18next'

// Client-side checks and formatting helpers for OS image files.
// Keep the extension allowlist in sync with the backend
// (backend/pkg/webapp/application/ports/in/commands/image_upload).

export const ALLOWED_IMAGE_EXTENSIONS = ['.iso', '.img', '.xz', '.zst']

export const IMAGE_FILE_ACCEPT = ALLOWED_IMAGE_EXTENSIONS.join(',')

// Returns a user-facing error message, or null if the file looks valid.
// maxSizeBytes (0/undefined = unlimited) comes from server capabilities so
// oversized files are rejected before any bytes leave the browser.
export const validateImageFile = (
  file: File,
  maxSizeBytes?: number,
): string | null => {
  if (file.size === 0) {
    return t('entities.osImage.lib.emptyFile')
  }
  const name = file.name.toLowerCase()
  if (!ALLOWED_IMAGE_EXTENSIONS.some((ext) => name.endsWith(ext))) {
    return t('entities.osImage.lib.unsupportedType', {
      allowed: ALLOWED_IMAGE_EXTENSIONS.join(', '),
    })
  }
  if (maxSizeBytes && file.size > maxSizeBytes) {
    return t('entities.osImage.lib.tooLargeWithSize', {
      size: formatFileSize(file.size),
      max: formatFileSize(maxSizeBytes),
    })
  }
  return null
}

// Short format label derived from the stored file's extension.
export const imageFormat = (
  filePath: string | undefined,
): 'RAW' | 'ISO' | 'XZ' | 'ZST' | '—' => {
  const ext = filePath?.toLowerCase().split('.').pop()
  switch (ext) {
    case 'img':
      return 'RAW'
    case 'iso':
      return 'ISO'
    case 'xz':
      return 'XZ'
    case 'zst':
      return 'ZST'
    default:
      return '—'
  }
}

export const formatUploadDate = (iso: string | undefined): string => {
  if (!iso) return '—'
  const date = new Date(iso)
  // A Go zero time serializes as year 1 — treat anything implausible as unset.
  if (Number.isNaN(date.getTime()) || date.getFullYear() < 2000) return '—'
  return date.toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  })
}

export const formatFileSize = (bytes: number): string => {
  if (bytes === 0) return t('entities.osImage.lib.zeroBytes')
  const k = 1024
  const sizes = [
    t('entities.osImage.lib.units.bytes'),
    t('entities.osImage.lib.units.kb'),
    t('entities.osImage.lib.units.mb'),
    t('entities.osImage.lib.units.gb'),
    t('entities.osImage.lib.units.tb'),
  ]
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return Math.round((bytes / Math.pow(k, i)) * 100) / 100 + ' ' + sizes[i]
}

type UploadErrorResponse = {
  response?: {
    status?: number
    data?: { message?: string; error?: string; max_size_mb?: string }
  }
  code?: string
  message?: string
}

export const isUploadCanceled = (error: unknown): boolean =>
  (error as UploadErrorResponse)?.code === 'ERR_CANCELED'

// Maps an API error to a message a user can act on.
export const getUploadErrorMessage = (
  error: unknown,
  fallback = t('entities.osImage.lib.uploadFailed'),
): string => {
  const err = error as UploadErrorResponse
  if (err?.response?.status === 413) {
    const maxSize = err.response.data?.max_size_mb
    return maxSize
      ? t('entities.osImage.lib.tooLargeMax', { max: maxSize })
      : t('entities.osImage.lib.tooLarge')
  }
  return (
    err?.response?.data?.message ||
    err?.response?.data?.error ||
    err?.message ||
    fallback
  )
}
