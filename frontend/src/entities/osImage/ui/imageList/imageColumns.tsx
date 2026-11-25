import { t } from 'i18next'

import { ImageResult } from '../../api/types'
import { imageFormat } from '../../lib'

// Column model for the image table, mirroring the node list approach:
// the header and the rows render from the same ordered definition.
export type ImageColumn = {
  key: 'format' | 'size' | 'uploaded' | 'version' | 'notes'
  label: string
  className: string
  // Sort accessor; string compare for text, numeric for sizes/dates.
  sortValue: (image: ImageResult) => string | number
}

// Each optional column appears only once the list is wide enough to hold it
// beside the name (w-64) and the columns already shown; the notes column,
// which flexes, needs 8rem of its own. imageColumns.test.ts checks the sums.
//
// A FUNCTION, not a const array: the labels come from i18next, which is not
// initialised when this module is first evaluated — resolving them at module
// load would freeze whatever the (empty) catalog answered. Call it at use
// time instead, exactly as nodeColumns.tsx does.
export const imageColumns = (): ImageColumn[] => [
  {
    key: 'format',
    label: t('entities.osImage.imageList.columns.format'),
    className: 'w-32 flex-shrink-0',
    sortValue: (image) => imageFormat(image.file_storage_info?.file_path),
  },
  {
    key: 'size',
    label: t('entities.osImage.imageList.columns.size'),
    className: 'w-24 flex-shrink-0',
    sortValue: (image) => image.file_storage_info?.size_bytes ?? 0,
  },
  {
    key: 'uploaded',
    label: t('entities.osImage.imageList.columns.uploaded'),
    className: 'w-28 flex-shrink-0 hidden @[44rem]:block',
    sortValue: (image) => {
      const iso = image.file_storage_info?.uploaded_at
      return iso ? new Date(iso).getTime() : 0
    },
  },
  {
    key: 'version',
    label: t('entities.osImage.imageList.columns.version'),
    className: 'w-24 flex-shrink-0 hidden @[52rem]:block',
    sortValue: (image) => image.version ?? '',
  },
  {
    key: 'notes',
    label: t('entities.osImage.imageList.columns.notes'),
    className: 'flex-1 min-w-[8rem] hidden @[60rem]:block',
    sortValue: (image) => image.description ?? '',
  },
]
