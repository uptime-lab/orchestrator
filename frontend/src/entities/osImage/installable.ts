import type { ImageResult } from './api/types'

// Why an image cannot be installed right now, mirroring what the server
// refuses before an install_os is queued: a record whose file is being
// replaced, one with no uploaded file, one whose checksum is not known yet.
export type InstallBlocker = 'busy' | 'noFile' | 'noChecksum'

const BUSY_STATUSES = new Set([
  'in_progress',
  'uploading',
  'capturing',
  'capture_uploading',
])

export const installBlocker = (
  image: Pick<
    ImageResult,
    'compression_status' | 'file_storage_info' | 'checksum'
  >,
): InstallBlocker | null => {
  if (image.compression_status && BUSY_STATUSES.has(image.compression_status)) {
    return 'busy'
  }
  if (!image.file_storage_info?.file_path) {
    return 'noFile'
  }
  if (!image.checksum) {
    return 'noChecksum'
  }
  return null
}

// The image to preselect for an install: the node's effective default
// (node, then unit, then account) when it is in the list and installable.
export const preselectedImageId = (
  images: Pick<
    ImageResult,
    'id' | 'compression_status' | 'file_storage_info' | 'checksum'
  >[],
  effectiveImageId: string | null | undefined,
): string => {
  if (!effectiveImageId) return ''
  const image = images.find((img) => img.id === effectiveImageId)
  return image && installBlocker(image) === null ? image.id : ''
}
