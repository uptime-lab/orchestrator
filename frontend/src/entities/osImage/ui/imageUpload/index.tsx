import clsx from 'clsx'
import {
  FC,
  useCallback,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
} from 'react'
import { useTranslation } from 'react-i18next'

import { IconDisk } from '@/shared/assets/icons'
import { ProgressBar } from '@/shared/ui/progressBar'

import type { UploadProgress } from '../../api/postUploadImageFile.ts'
import {
  formatFileSize,
  IMAGE_FILE_ACCEPT,
  validateImageFile,
} from '../../lib.ts'

const UploadIcon = () => (
  <svg
    className="w-6 h-6 text-neutral-6"
    fill="none"
    stroke="currentColor"
    viewBox="0 0 24 24"
    xmlns="http://www.w3.org/2000/svg"
  >
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      strokeWidth={2}
      d="M7 16a4 4 0 01-.88-7.903A5 5 0 1115.9 6L16 6a5 5 0 011 9.9M15 13l-3-3m0 0l-3 3m3-3v12"
    />
  </svg>
)

type ImageDropzoneProps = {
  selectedFile: File | null
  onSelect: (file: File) => void
  onClear: () => void
  onValidationError: (message: string) => void
  disabled?: boolean
  hint?: string
  // Server-advertised upload cap in bytes (0/undefined = unlimited).
  maxSizeBytes?: number
}

// Drag-and-drop file picker with client-side OS image checks.
export const ImageDropzone: FC<ImageDropzoneProps> = ({
  selectedFile,
  onSelect,
  onClear,
  onValidationError,
  disabled,
  hint,
  maxSizeBytes,
}) => {
  const { t } = useTranslation()
  const [isDragging, setIsDragging] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleFile = useCallback(
    (file: File) => {
      const error = validateImageFile(file, maxSizeBytes)
      if (error) {
        onValidationError(error)
        return
      }
      onSelect(file)
    },
    [onSelect, onValidationError, maxSizeBytes],
  )

  const handleDragOver = useCallback(
    (e: DragEvent<HTMLDivElement>) => {
      e.preventDefault()
      if (!disabled) {
        setIsDragging(true)
      }
    },
    [disabled],
  )

  const handleDragLeave = useCallback(() => {
    setIsDragging(false)
  }, [])

  const handleDrop = useCallback(
    (e: DragEvent<HTMLDivElement>) => {
      e.preventDefault()
      setIsDragging(false)
      if (disabled) {
        return
      }
      const file = e.dataTransfer.files[0]
      if (file) {
        handleFile(file)
      }
    },
    [disabled, handleFile],
  )

  const handleInputChange = useCallback(
    (e: ChangeEvent<HTMLInputElement>) => {
      const file = e.target.files?.[0]
      if (file) {
        handleFile(file)
      }
    },
    [handleFile],
  )

  const handleClear = useCallback(() => {
    onClear()
    if (fileInputRef.current) {
      fileInputRef.current.value = ''
    }
  }, [onClear])

  return (
    <div
      className={clsx(
        'border-2 border-dashed rounded p-4 flex flex-col items-center justify-center gap-2 transition-colors',
        disabled ? 'cursor-not-allowed opacity-60' : 'cursor-pointer',
        isDragging
          ? 'border-lime-6 bg-lime-6/10'
          : 'border-neutral-11 hover:border-neutral-10',
      )}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
      onClick={() => {
        if (!disabled) {
          fileInputRef.current?.click()
        }
      }}
    >
      {selectedFile ? (
        <>
          <IconDisk className="w-6 h-6 text-neutral-6" />
          <div className="flex flex-col items-center gap-1">
            <div className="text-sm text-neutral-1 font-medium break-all text-center">
              {selectedFile.name}
            </div>
            <div className="text-xs text-neutral-4">
              {formatFileSize(selectedFile.size)}
            </div>
          </div>
          {!disabled && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                handleClear()
              }}
              className="text-xs text-neutral-4 hover:text-neutral-2"
            >
              {t('entities.osImage.imageUpload.remove')}
            </button>
          )}
        </>
      ) : (
        <>
          <UploadIcon />
          <div className="text-xs text-neutral-4 text-center">
            {t('entities.osImage.imageUpload.dragAndDrop')}{' '}
            <span className="text-lime-6 underline cursor-pointer">
              {t('entities.osImage.imageUpload.chooseOne')}
            </span>
            {hint ? ` ${hint}` : ''}
          </div>
          <div className="text-xs text-neutral-5">
            {t('entities.osImage.imageUpload.supportedFormats')}
          </div>
        </>
      )}
      <input
        ref={fileInputRef}
        type="file"
        className="hidden"
        onChange={handleInputChange}
        accept={IMAGE_FILE_ACCEPT}
        disabled={disabled}
      />
    </div>
  )
}

type ImageUploadProgressProps = {
  fileName: string
  progress: UploadProgress
  onCancel: () => void
}

// Progress panel shown while an image file is being uploaded.
export const ImageUploadProgress: FC<ImageUploadProgressProps> = ({
  fileName,
  progress,
  onCancel,
}) => {
  const { t } = useTranslation()
  const finalizing = progress.percent >= 100
  return (
    <div className="flex flex-col gap-2 bg-neutral-13 rounded px-3 py-3">
      <div className="flex items-center justify-between gap-2">
        <div className="text-sm text-neutral-1 break-all">{fileName}</div>
        <button
          type="button"
          onClick={onCancel}
          className="text-xs text-neutral-4 hover:text-neutral-2 shrink-0"
        >
          {t('entities.osImage.imageUpload.cancel')}
        </button>
      </div>
      <ProgressBar percent={progress.percent} />
      <div className="text-xs text-neutral-4">
        {finalizing
          ? t('entities.osImage.imageUpload.finalizing')
          : t('entities.osImage.imageUpload.progress', {
              percent: progress.percent,
              loaded: formatFileSize(progress.loadedBytes),
              total: formatFileSize(progress.totalBytes),
            })}
      </div>
    </div>
  )
}
