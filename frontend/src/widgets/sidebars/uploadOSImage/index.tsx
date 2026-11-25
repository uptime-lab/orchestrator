import { useNavigate } from '@tanstack/react-router'
import { FC, useRef, useState, type SyntheticEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { SidebarEnum } from '@/widgets/sidebars/types'

import {
  apiImages,
  getUploadErrorMessage,
  imagesModel,
  isUploadCanceled,
  type UploadProgress,
} from '@/entities/osImage'
import {
  ImageDropzone,
  ImageUploadProgress,
} from '@/entities/osImage/ui/imageUpload'
import { useServerCapabilities } from '@/entities/serverCapabilities'

import { IconDisk } from '@/shared/assets/icons'
import { useUnloadGuard } from '@/shared/hooks/useUnloadGuard'
import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'

import { SidebarProps } from '../types'

export const SidebarUploadOSImage: FC<SidebarProps> = ({ onClose }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const caps = useServerCapabilities()
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [selectedFile, setSelectedFile] = useState<File | null>(null)
  const [uploadProgress, setUploadProgress] = useState<UploadProgress | null>(
    null,
  )
  const abortRef = useRef<AbortController | null>(null)

  useUnloadGuard(isProcessing)

  const handleFormSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setFormError(null)

    if (!selectedFile) {
      setFormError(t('widgets.sidebars.uploadOSImage.selectFileError'))
      return
    }

    const data = new FormData(event.target as HTMLFormElement)
    const title = (data.get('title') || '').toString().trim()
    const version = (data.get('version') || '').toString().trim()
    const description = (data.get('description') || '').toString().trim()

    if (!title) {
      setFormError(t('widgets.sidebars.uploadOSImage.titleRequired'))
      return
    }

    setIsProcessing(true)

    // Create the image record first, then stream the file into it. If the
    // file upload fails, remove the record so the list has no file-less
    // orphans and the user can simply retry.
    let createdImageId: string | null = null
    try {
      const newImage = await apiImages.postCreateImage({
        title,
        version: version || undefined,
        description: description || undefined,
      })
      createdImageId = newImage.id

      const abortController = new AbortController()
      abortRef.current = abortController
      setUploadProgress({
        loadedBytes: 0,
        totalBytes: selectedFile.size,
        percent: 0,
      })

      await apiImages.postUploadImageFile(newImage.id, selectedFile, {
        signal: abortController.signal,
        onProgress: setUploadProgress,
      })

      await imagesModel.fetchImagesFx()

      // Merge into the current search rather than replacing it: this sidebar
      // now runs under /workflows, where dropping the `tab` param would reset
      // the page to the Playbooks tab mid-upload-flow.
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          sidebar: SidebarEnum.OSImageDetails,
          imageId: newImage.id,
        }),
        replace: true,
      })
    } catch (error: unknown) {
      if (createdImageId) {
        try {
          await apiImages.deleteImage(createdImageId)
          await imagesModel.fetchImagesFx()
        } catch {
          // Deleting the orphan is best effort; the user can remove it manually.
        }
      }
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

  return (
    <div className="px-5 pt-0.5 pb-5 flex flex-col gap-6">
      <div className="text-xs text-neutral-4">
        {t('widgets.sidebars.uploadOSImage.breadcrumb')}
      </div>
      <h2 className="text-lg font-bold uppercase">
        {t('widgets.sidebars.uploadOSImage.title')}
      </h2>

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
          label={t('widgets.sidebars.uploadOSImage.titleLabel')}
          placeholder={t('widgets.sidebars.uploadOSImage.titlePlaceholder')}
          required
          disabled={isProcessing}
        />

        <Input
          id="version"
          name="version"
          type="text"
          label={t('widgets.sidebars.uploadOSImage.versionLabel')}
          placeholder={t('widgets.sidebars.uploadOSImage.versionPlaceholder')}
          disabled={isProcessing}
        />

        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <label className="text-sm text-neutral-6">
              {t('widgets.sidebars.uploadOSImage.notesLabel')}
            </label>
            <span className="text-xs text-neutral-4">
              {t('widgets.sidebars.uploadOSImage.optional')}
            </span>
          </div>
          <Input
            id="description"
            name="description"
            type="text"
            placeholder={t('widgets.sidebars.uploadOSImage.notesPlaceholder')}
            disabled={isProcessing}
          />
        </div>

        <div className="flex flex-col gap-2">
          <div className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.uploadOSImage.imageLabel')}
          </div>
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
              disabled={isProcessing}
              maxSizeBytes={caps.max_image_upload_size}
            />
          )}
        </div>

        {formError && <div className="text-sm text-red-6">{formError}</div>}

        <div className="flex gap-3">
          <Button
            size="lg"
            variant={formError ? 'destructive' : 'normal'}
            type="submit"
            disabled={isProcessing || !selectedFile}
            grow
          >
            {isProcessing
              ? t('widgets.sidebars.uploadOSImage.uploading')
              : t('widgets.sidebars.uploadOSImage.done')}
          </Button>
          <Button
            size="lg"
            kind="secondary"
            type="button"
            onClick={onClose}
            disabled={isProcessing}
          >
            {t('widgets.sidebars.uploadOSImage.cancel')}
          </Button>
        </div>
      </form>
    </div>
  )
}
