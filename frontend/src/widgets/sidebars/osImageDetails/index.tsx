import {
  FC,
  useCallback,
  useEffect,
  useRef,
  useState,
  type SyntheticEvent,
} from 'react'
import { useTranslation } from 'react-i18next'

import {
  apiImages,
  formatFileSize,
  getUploadErrorMessage,
  imagesModel,
  isUploadCanceled,
  type UploadProgress,
} from '@/entities/osImage'
import { useImage } from '@/entities/osImage/hooks'
import {
  ImageDropzone,
  ImageUploadProgress,
} from '@/entities/osImage/ui/imageUpload'
import { Actions, useCan } from '@/entities/permissions/hooks'

import { IconDisk } from '@/shared/assets/icons'
import { useUnloadGuard } from '@/shared/hooks/useUnloadGuard'
import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { Input } from '@/shared/ui/input'
import { ProgressBar } from '@/shared/ui/progressBar'
import { clearsRequiredTitle } from '@/shared/utils/formRules'

import { SidebarProps } from '../types'

export const SidebarOSImageDetails: FC<SidebarProps & { imageId: string }> = ({
  imageId,
  onClose,
}) => {
  const { t } = useTranslation()
  const { image } = useImage({ imageId })
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [titleMissing, setTitleMissing] = useState(false)
  const [isDeleteConfirmOpen, setIsDeleteConfirmOpen] = useState(false)
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [uploadProgress, setUploadProgress] = useState<UploadProgress | null>(
    null,
  )
  const abortRef = useRef<AbortController | null>(null)

  useUnloadGuard(isProcessing)

  useEffect(() => {
    // Fetch the image if not in store
    if (!image) {
      imagesModel.fetchImageFx(imageId)
    }
  }, [imageId, image])

  const fileInfo = image?.file_storage_info
  // The deploy format is multi-block xz. Raw files are compressed, .zst and
  // uploaded (single-block) .xz files are repacked; a file that already
  // references a retained source is our own output and is final.
  const fileExt = fileInfo?.file_path?.toLowerCase().split('.').pop()
  const isCompressedFile = fileExt === 'xz' || fileExt === 'zst'
  // Every mutation here — recompress, rename, delete — is the same account-wide
  // image grant. A viewer sees the details and nothing that can only 403.
  const canManage = useCan()(Actions.ImageManage)
  const canCompress =
    canManage &&
    !!fileInfo &&
    !(isCompressedFile && !!fileInfo.source_file_path)
  const compressLabel = !isCompressedFile
    ? t('widgets.sidebars.osImageDetails.compressImage')
    : fileExt === 'zst'
      ? t('widgets.sidebars.osImageDetails.repackToXz')
      : t('widgets.sidebars.osImageDetails.repackToMultiBlockXz')
  const compressHint = !isCompressedFile
    ? t('widgets.sidebars.osImageDetails.compressHintRaw')
    : fileExt === 'zst'
      ? t('widgets.sidebars.osImageDetails.compressHintZst')
      : t('widgets.sidebars.osImageDetails.compressHintXz')
  const isCompressing = image?.compression_status === 'in_progress'
  const compressionFailed = image?.compression_status === 'failed'

  // Taking the image back out. Two steps on purpose: the API call carries the
  // JWT and returns a short-lived self-authenticating URL, and only then does
  // the browser navigate — a navigation cannot send an Authorization header, and
  // pulling gigabytes through axios would buffer the whole image in the tab.
  const [isPreparingDownload, setIsPreparingDownload] = useState(false)
  const handleDownload = useCallback(async () => {
    setFormError(null)
    setIsPreparingDownload(true)
    try {
      const { url } = await apiImages.getImageDownload(imageId)
      // assign, not a synthetic <a download>: the file is cross-origin in every
      // deployment where storage is a bucket, and the download attribute is
      // ignored there. The server sets Content-Disposition instead.
      window.location.assign(url)
    } catch (error) {
      setFormError(
        getUploadErrorMessage(
          error,
          t('widgets.sidebars.osImageDetails.downloadError'),
        ),
      )
    } finally {
      setIsPreparingDownload(false)
    }
  }, [imageId])

  const handleCompress = useCallback(async () => {
    setFormError(null)
    try {
      await apiImages.postCompressImage(imageId)
      await imagesModel.fetchImageFx(imageId)
    } catch (error) {
      setFormError(
        getUploadErrorMessage(
          error,
          t('widgets.sidebars.osImageDetails.compressStartError'),
        ),
      )
    }
  }, [imageId])

  const handleFormSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setFormError(null)

    const data = new FormData(event.target as HTMLFormElement)
    const title = (data.get('title') || '').toString().trim()
    const version = (data.get('version') || '').toString().trim()
    const description = (data.get('description') || '').toString().trim()

    if (clearsRequiredTitle(title, image?.title ?? '')) {
      setTitleMissing(true)
      return
    }
    setTitleMissing(false)
    setIsProcessing(true)

    try {
      // Update image metadata
      await apiImages.putUpdateImage(imageId, {
        title: title || undefined,
        version: version || undefined,
        description: description || undefined,
      })

      // Upload file if selected
      if (selectedFile) {
        const abortController = new AbortController()
        abortRef.current = abortController
        setUploadProgress({
          loadedBytes: 0,
          totalBytes: selectedFile.size,
          percent: 0,
        })
        await apiImages.postUploadImageFile(imageId, selectedFile, {
          signal: abortController.signal,
          onProgress: setUploadProgress,
        })
      }

      await imagesModel.fetchImagesFx()
      // Refresh the current image
      await imagesModel.fetchImageFx(imageId)
      setSelectedFile(null)
    } catch (error: unknown) {
      if (!isUploadCanceled(error)) {
        setFormError(getUploadErrorMessage(error))
      }
    } finally {
      abortRef.current = null
      setUploadProgress(null)
      setIsProcessing(false)
    }
  }

  const handleCancelUpload = () => {
    abortRef.current?.abort()
  }

  const handleDeleteImage = useCallback(async () => {
    setIsDeleteConfirmOpen(false)
    setIsProcessing(true)
    try {
      await apiImages.deleteImage(imageId)
      await imagesModel.fetchImagesFx()
      onClose()
    } catch (error) {
      setFormError(
        getUploadErrorMessage(
          error,
          t('widgets.sidebars.osImageDetails.deleteImageError'),
        ),
      )
    } finally {
      setIsProcessing(false)
    }
  }, [imageId, onClose])

  if (!image) {
    return null
  }

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
      <div className="flex items-center gap-3">
        <button
          type="button"
          onClick={onClose}
          aria-label={t('widgets.sidebars.osImageDetails.closePanel')}
          title={t('widgets.sidebars.osImageDetails.closePanel')}
          className="flex items-center justify-center w-6 h-6 rounded border border-neutral-11 text-neutral-6 hover:text-neutral-2 hover:border-neutral-9 transition-colors flex-shrink-0"
        >
          <svg
            className="w-3.5 h-3.5"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M9 5l7 7-7 7"
            />
          </svg>
        </button>
        <h2 className="text-lg font-bold uppercase">
          {t('widgets.sidebars.osImageDetails.title')}
        </h2>
      </div>

      <div className="flex justify-center">
        <div className="w-20 h-20 rounded bg-neutral-13 flex items-center justify-center">
          <IconDisk className="w-10 h-10 text-neutral-6" />
        </div>
      </div>

      <form className="flex flex-col gap-5" onSubmit={handleFormSubmit}>
        <Input
          id="title"
          name="title"
          type="text"
          label={t('widgets.sidebars.osImageDetails.titleLabel')}
          placeholder={t('widgets.sidebars.osImageDetails.titlePlaceholder')}
          defaultValue={image.title}
          disabled={isProcessing}
          error={titleMissing}
          aria-invalid={titleMissing}
          onChange={() => setTitleMissing(false)}
        />
        {titleMissing && (
          <div className="text-xs text-red-6 -mt-3">
            {t('widgets.sidebars.osImageDetails.titleRequired')}
          </div>
        )}

        <Input
          id="version"
          name="version"
          type="text"
          label={t('widgets.sidebars.osImageDetails.versionLabel')}
          placeholder={t('widgets.sidebars.osImageDetails.versionPlaceholder')}
          defaultValue={image.version || ''}
          disabled={isProcessing}
        />

        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <label className="text-sm text-neutral-6">
              {t('widgets.sidebars.osImageDetails.notesLabel')}
            </label>
            <span className="text-xs text-neutral-4">
              {t('widgets.sidebars.osImageDetails.optional')}
            </span>
          </div>
          <Input
            id="description"
            name="description"
            type="text"
            placeholder={t('widgets.sidebars.osImageDetails.notesPlaceholder')}
            defaultValue={image.description || ''}
            disabled={isProcessing}
          />
        </div>

        {image.kind && image.kind !== 'os' && (
          <div className="flex flex-col gap-1 bg-neutral-13 rounded px-3 py-2">
            <div className="text-xs text-neutral-4 uppercase">
              {image.kind === 'golden'
                ? t('widgets.sidebars.osImageDetails.goldenImage')
                : t('widgets.sidebars.osImageDetails.backup')}
            </div>
            <div className="text-sm text-neutral-1">
              {t('widgets.sidebars.osImageDetails.capturedFrom', {
                source:
                  image.source_node_name ||
                  t('widgets.sidebars.osImageDetails.aManagedNode'),
              })}
            </div>
            {image.kind === 'backup' && (
              <div className="text-xs text-neutral-4">
                {t('widgets.sidebars.osImageDetails.backupRestoreNote')}
              </div>
            )}
          </div>
        )}

        <div className="flex flex-col gap-2">
          <div className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.osImageDetails.imageSectionLabel')}
          </div>
          {fileInfo ? (
            <div className="flex flex-col gap-1 bg-neutral-13 rounded px-3 py-2">
              <div className="flex items-center gap-2">
                <IconDisk className="w-4 h-4 text-neutral-6 shrink-0" />
                <div className="text-sm text-neutral-1 break-all">
                  {fileInfo.file_name ||
                    t('widgets.sidebars.osImageDetails.fileUploaded')}
                </div>
              </div>
              {!!fileInfo.size_bytes && (
                <div className="text-xs text-neutral-4">
                  {formatFileSize(fileInfo.size_bytes)}
                </div>
              )}
              {image.checksum && (
                <div
                  className="text-xs text-neutral-4 break-all"
                  title={image.checksum}
                >
                  SHA256: {image.checksum.slice(0, 16)}…
                </div>
              )}
              {fileInfo.source_file_path && (
                <div className="text-xs text-neutral-4">
                  {t('widgets.sidebars.osImageDetails.originalKept', {
                    fileName: fileInfo.source_file_path.split('/').pop() ?? '',
                    sizeSuffix: fileInfo.source_size_bytes
                      ? ` (${formatFileSize(fileInfo.source_size_bytes)})`
                      : '',
                  })}
                </div>
              )}
              {isCompressing && (
                <div className="flex flex-col gap-1 pt-1">
                  <ProgressBar percent={image.compression_progress ?? 0} />
                  <div className="text-xs text-lime-6">
                    {t('widgets.sidebars.osImageDetails.compressing', {
                      percent: image.compression_progress ?? 0,
                    })}
                  </div>
                </div>
              )}
              {compressionFailed && (
                <div className="text-xs text-red-6">
                  {t('widgets.sidebars.osImageDetails.compressionFailed')}
                </div>
              )}
              {canManage && !isCompressing && (
                <button
                  type="button"
                  onClick={handleDownload}
                  disabled={isProcessing || isPreparingDownload}
                  className="text-xs text-lime-6 underline text-left hover:text-lime-5 w-fit"
                >
                  {isPreparingDownload
                    ? t('widgets.sidebars.osImageDetails.preparingDownload')
                    : t('widgets.sidebars.osImageDetails.downloadImage')}
                  <span className="text-neutral-4 no-underline">
                    {t('widgets.sidebars.osImageDetails.downloadHint', {
                      sizeSuffix: fileInfo.size_bytes
                        ? ` (${formatFileSize(fileInfo.size_bytes)})`
                        : '',
                    })}
                  </span>
                </button>
              )}
              {canCompress && !isCompressing && (
                <button
                  type="button"
                  onClick={handleCompress}
                  disabled={isProcessing}
                  className="text-xs text-lime-6 underline text-left hover:text-lime-5 w-fit"
                >
                  {compressionFailed
                    ? t('widgets.sidebars.osImageDetails.retry')
                    : compressLabel}
                  <span className="text-neutral-4 no-underline">
                    {compressHint}
                  </span>
                </button>
              )}
            </div>
          ) : (
            <div className="text-sm text-neutral-4">
              {t('widgets.sidebars.osImageDetails.noFileUploaded')}
            </div>
          )}
          {uploadProgress && selectedFile ? (
            <ImageUploadProgress
              fileName={selectedFile.name}
              progress={uploadProgress}
              onCancel={handleCancelUpload}
            />
          ) : (
            <ImageDropzone
              selectedFile={selectedFile}
              onSelect={(file) => {
                setSelectedFile(file)
                setFormError(null)
              }}
              onClear={() => setSelectedFile(null)}
              onValidationError={setFormError}
              disabled={isProcessing || isCompressing}
              hint={
                fileInfo
                  ? t('widgets.sidebars.osImageDetails.toReplace')
                  : undefined
              }
            />
          )}
        </div>

        {formError && <div className="text-sm text-red-6">{formError}</div>}

        <div className="flex gap-3">
          {canManage && (
            <Button
              size="lg"
              variant={formError ? 'destructive' : 'normal'}
              type="submit"
              disabled={isProcessing}
              grow
            >
              {isProcessing
                ? t('widgets.sidebars.osImageDetails.saving')
                : t('widgets.sidebars.osImageDetails.saveChanges')}
            </Button>
          )}
          <Button
            size="lg"
            kind="secondary"
            type="button"
            onClick={onClose}
            disabled={isProcessing}
          >
            {canManage
              ? t('widgets.sidebars.osImageDetails.cancel')
              : t('widgets.sidebars.osImageDetails.close')}
          </Button>
        </div>
      </form>

      {canManage && (
        <Button
          size="lg"
          variant="destructive"
          kind="primary"
          type="button"
          onClick={() => setIsDeleteConfirmOpen(true)}
          disabled={isProcessing}
          grow
        >
          {t('widgets.sidebars.osImageDetails.deleteButton')}
        </Button>
      )}
      <ConfirmDialog
        open={isDeleteConfirmOpen}
        title={t('widgets.sidebars.osImageDetails.deleteConfirmTitle')}
        message={
          // A delete stops a queued or running compression first; the server
          // waits for the job to let go of the image, which takes a moment.
          isCompressing
            ? t('widgets.sidebars.osImageDetails.deleteConfirmCompressing')
            : t('widgets.sidebars.osImageDetails.deleteConfirmMessage')
        }
        confirmText={t('widgets.sidebars.osImageDetails.delete')}
        destructive
        onConfirm={handleDeleteImage}
        onCancel={() => setIsDeleteConfirmOpen(false)}
      />
    </div>
  )
}
