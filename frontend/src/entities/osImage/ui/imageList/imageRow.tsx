import clsx from 'clsx'
import { createLocator, Locator, Mark } from 'create-locator'
import { t } from 'i18next'

import { IconDisk } from '@/shared/assets/icons'
import { ProgressBar } from '@/shared/ui/progressBar'

import { ImageResult } from '../../api/types'
import { formatFileSize, formatUploadDate, imageFormat } from '../../lib'
import { imageColumns, type ImageColumn } from './imageColumns'

// dataTransfer key for dragging an image row onto the OU rail.
export const IMAGE_DND_TYPE = 'text/os-image-id'

export type ImageRowLocator = Locator<{
  id: { id: string }
  title: { title: string }
}>

type ImageRowProps = {
  id: string
  image: ImageResult
  selected: boolean
  // True when some OU uses this image as its default install image.
  isDefaultForOU?: boolean
  // True when the account policy names this image as its default install
  // image (what a node outside every unit, or a unit that sets none, gets).
  isAccountDefault?: boolean
  onClick?: (id: string) => unknown
} & Mark<ImageRowLocator>

const formatBadgeClass: Record<string, string> = {
  RAW: 'text-orange-5 border-orange-5',
  ISO: 'text-orange-5 border-orange-5',
  XZ: 'text-blue-5 border-blue-8',
  ZST: 'text-lime-5 border-lime-7',
  '—': 'text-neutral-6 border-neutral-11',
}

// Captured images get a kind chip; plain uploaded OS images stay unmarked.
const kindBadgeClass: Record<string, string> = {
  golden: 'text-yellow-5 border-yellow-6',
  backup: 'text-purple-4 border-purple-5',
}

const cellContent = (image: ImageResult, column: ImageColumn) => {
  const fileInfo = image.file_storage_info
  switch (column.key) {
    case 'format': {
      if (image.compression_status === 'in_progress') {
        return (
          <div className="flex items-center gap-2 min-w-0">
            <div className="w-14 flex-shrink-0">
              <ProgressBar percent={image.compression_progress ?? 0} />
            </div>
            <span className="text-xs text-lime-6">
              {image.compression_progress ?? 0}%
            </span>
          </div>
        )
      }
      // A file being replaced right now: the format shown would describe the
      // file that is on its way out, so say what is happening instead. Seen by
      // everyone EXCEPT the person uploading, who is watching their own progress.
      if (image.compression_status === 'uploading') {
        return (
          <span className="text-xs text-neutral-6">
            {t('entities.osImage.imageRow.uploading')}
          </span>
        )
      }
      // A capture in flight, and a capture that died. The record has no file
      // yet in either case, so without these both rendered exactly like an
      // empty image: a device streaming a multi-GB disk and a capture that
      // failed hours ago were indistinguishable from a record nobody has
      // uploaded to. capture_uploading is the device's stream actually
      // arriving; capturing is the window before it starts.
      if (
        image.compression_status === 'capturing' ||
        image.compression_status === 'capture_uploading'
      ) {
        return (
          <span className="text-xs text-lime-6">
            {t('entities.osImage.imageRow.capturing')}
          </span>
        )
      }
      if (image.compression_status === 'capture_failed') {
        return (
          <span className="text-xs text-red-6">
            {t('entities.osImage.imageRow.captureFailed')}
          </span>
        )
      }
      const format = imageFormat(fileInfo?.file_path)
      return (
        <span className="flex items-center gap-1.5">
          <span
            className={clsx(
              'border rounded-[3px] px-1.5 py-px text-xxs',
              formatBadgeClass[format],
            )}
          >
            {format}
          </span>
          {image.compression_status === 'failed' && (
            <span className="text-xxs text-red-6">
              {t('entities.osImage.imageRow.compressionFailed')}
            </span>
          )}
        </span>
      )
    }
    case 'size':
      return fileInfo?.size_bytes ? formatFileSize(fileInfo.size_bytes) : '—'
    case 'uploaded':
      return formatUploadDate(fileInfo?.uploaded_at)
    case 'version':
      return image.version || '—'
    case 'notes':
      return (
        <span className="truncate block" title={image.description}>
          {image.description || '—'}
        </span>
      )
    default:
      return null
  }
}

export const ImageRow = ({
  id,
  image,
  selected,
  isDefaultForOU = false,
  isAccountDefault = false,
  onClick,
  ...rest
}: ImageRowProps) => {
  const locator = createLocator(rest)

  return (
    <div
      draggable
      onDragStart={(e) => {
        e.dataTransfer.setData(IMAGE_DND_TYPE, id)
        e.dataTransfer.effectAllowed = 'move'
      }}
      className={clsx(
        'flex items-center gap-4 rounded px-4 py-3 cursor-pointer transition-colors',
        'cursor-grab active:cursor-grabbing',
        selected ? 'bg-[#3C4B12]' : 'bg-neutral-14 hover:bg-neutral-13',
      )}
      onClick={() => onClick?.(id)}
      {...locator()}
    >
      <div className="flex items-center gap-3 w-64 flex-shrink-0 min-w-0">
        <div className="w-8 h-8 rounded bg-neutral-13 flex items-center justify-center flex-shrink-0">
          <IconDisk className="w-5 h-5 text-neutral-4" />
        </div>
        <div className="min-w-0">
          <div
            className="font-mono font-bold text-sm text-neutral-1 truncate"
            {...locator.title({ title: image.title })}
          >
            {image.title}
          </div>
          <div className="flex items-center gap-1">
            {image.kind && image.kind !== 'os' && (
              <span
                className={clsx(
                  'inline-block mt-0.5 border rounded-[3px] px-1.5 py-px text-xxs uppercase',
                  kindBadgeClass[image.kind],
                )}
                title={
                  image.source_node_name
                    ? t('entities.osImage.imageRow.capturedFrom', {
                        node: image.source_node_name,
                      })
                    : undefined
                }
              >
                {image.kind}
              </span>
            )}
            {isAccountDefault && (
              <span className="inline-block mt-0.5 border border-lime-7 text-lime-5 rounded-[3px] px-1.5 py-px text-xxs uppercase">
                {t('entities.osImage.imageRow.accountDefault')}
              </span>
            )}
            {isDefaultForOU && (
              <span className="inline-block mt-0.5 border border-lime-7 text-lime-5 rounded-[3px] px-1.5 py-px text-xxs uppercase">
                {t('entities.osImage.imageRow.ouDefault')}
              </span>
            )}
          </div>
          {image.file_storage_info?.file_name && (
            <div className="font-mono text-xxs text-neutral-6 truncate">
              {image.file_storage_info.file_name}
            </div>
          )}
        </div>
      </div>
      <div className="flex items-center gap-6 flex-1 min-w-0">
        {imageColumns().map((column) => (
          <div
            key={column.key}
            className={clsx(
              'font-mono text-sm text-neutral-3',
              column.className,
            )}
          >
            {cellContent(image, column)}
          </div>
        ))}
      </div>
    </div>
  )
}
