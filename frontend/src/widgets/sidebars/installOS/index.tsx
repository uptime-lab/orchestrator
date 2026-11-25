import { useNavigate } from '@tanstack/react-router'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SubViewHeader } from '@/widgets/sidebars/node/subViewHeader'
import { SidebarEnum } from '@/widgets/sidebars/types'

import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import { useNode } from '@/entities/node/hooks.ts'
import { imagesModel } from '@/entities/osImage'
import { useImages } from '@/entities/osImage/hooks'
import {
  installBlocker,
  preselectedImageId,
} from '@/entities/osImage/installable'
import { apiOUs } from '@/entities/ou'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { extractErrorText, showSuccess } from '@/shared/ui/toast'

import { SidebarProps } from '../types'
import { installRequestFromPanel } from './installRequest'

export const SidebarInstallOS: FC<
  SidebarProps & {
    nodeId: string
    onDone?: () => void
    // Title shown in the header — "Install OS" by default, "Restore from backup"
    // when opened from the restore entry (same picker, different framing).
    heading?: string
    // Preselect the node's effective default install image (node, then unit,
    // then account). Off for a restore, where the default OS image is not
    // what the operator came for.
    preselectDefault?: boolean
  }
> = ({ nodeId, onDone, heading, preselectDefault = false }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { node } = useNode({ nodeId })
  const { images } = useImages()
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [selectedImageId, setSelectedImageId] = useState<string>('')
  const [selectedDevice, setSelectedDevice] = useState<string>('')
  // Writing an image erases the target device, and nothing on this panel
  // used to say so — one click on the submit button dispatched it. The
  // sibling 'Erase drive' control makes the operator type the device path;
  // this is the same destruction reached by an easier route.
  const [confirmOpen, setConfirmOpen] = useState(false)

  const headingText = heading ?? t('widgets.sidebars.installOS.defaultHeading')

  // The effective default image, resolved by the server along the node's
  // unit chain. Only read when this panel preselects it.
  const [defaultImageId, setDefaultImageId] = useState<string | null>(null)
  useEffect(() => {
    if (!preselectDefault) return
    let cancelled = false
    apiOUs
      .getEffectivePolicy(nodeId)
      .then((result) => {
        if (!cancelled) {
          setDefaultImageId(result.os_image_id?.value ?? null)
        }
      })
      .catch(() => {
        // No preselection; the picker still works.
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, preselectDefault])

  // Preselect once, and never over the operator's own choice.
  const [preselected, setPreselected] = useState(false)
  useEffect(() => {
    if (preselected || selectedImageId || !defaultImageId) return
    const id = preselectedImageId(images, defaultImageId)
    if (id) {
      setSelectedImageId(id)
      setPreselected(true)
    }
  }, [preselected, selectedImageId, defaultImageId, images])

  // Fetch images on mount if not already loaded
  useEffect(() => {
    if (images.length === 0) {
      imagesModel.fetchImagesFx()
    }
  }, [images.length])

  const handleBack = useCallback(() => {
    // Embedded inside the node Actions tab: hand control back to the tab instead
    // of navigating the whole panel away.
    if (onDone) {
      onDone()
      return
    }
    navigate({
      to: '.',
      search: (old) => ({
        ...old,
        sidebar: SidebarEnum.Node,
        nodeId,
      }),
    })
  }, [navigate, nodeId, onDone])

  // The form no longer dispatches: it asks first. Everything below runs only
  // after the operator has read what the target device is and confirmed it.
  const handleFormSubmit = useCallback(
    (event: React.FormEvent<HTMLFormElement>) => {
      event.preventDefault()
      setFormError(null)

      if (!selectedImageId) {
        setFormError(t('widgets.sidebars.installOS.selectImageError'))
        return
      }

      if (!selectedDevice) {
        setFormError(t('widgets.sidebars.installOS.selectTargetDeviceError'))
        return
      }

      setConfirmOpen(true)
    },
    [selectedImageId, selectedDevice, t],
  )

  const runInstall = useCallback(async () => {
    setConfirmOpen(false)
    setFormError(null)
    setIsProcessing(true)

    try {
      await apiNodes.postInstallOS(
        nodeId,
        installRequestFromPanel(selectedImageId, selectedDevice),
      )

      // Refresh nodes to get updated status
      await nodesModel.fetchNodesFx()

      showSuccess(
        t('widgets.sidebars.installOS.startedTitle', { heading: headingText }),
        t('widgets.sidebars.installOS.startedMessage', {
          device: selectedDevice,
          nodeName: node?.name || t('widgets.sidebars.installOS.thisNode'),
        }),
      )
      // Navigate back to node sidebar
      handleBack()
    } catch (error: unknown) {
      setFormError(
        extractErrorText(error) ??
          t('widgets.sidebars.installOS.installFailed'),
      )
    } finally {
      setIsProcessing(false)
    }
  }, [
    selectedImageId,
    selectedDevice,
    nodeId,
    handleBack,
    headingText,
    node?.name,
    t,
  ])

  // Get storage devices from node
  // Prefer system_info, like every other consumer (captureImage, the node
  // panel, the fleet card, entities/node/utils). Reading only the top-level
  // field made THIS picker — the one attached to the operation that erases a
  // disk — show a different or empty device list from the one Advanced →
  // Storage shows two clicks away.
  const storageDevices =
    node?.system_info?.storage_devices || node?.storage_devices || {}
  const deviceEntries = Object.entries(storageDevices)

  // Split images so the same picker serves both a fresh install and a restore:
  // OS images vs this account's captured backups/golden templates.
  const osImages = images.filter((img) => !img.kind || img.kind === 'os')
  const captureImages = images.filter(
    (img) => img.kind === 'backup' || img.kind === 'golden',
  )
  // An image the server would refuse is listed but not selectable, with the
  // reason in its label: an entry that silently fails after confirmation is
  // worse than one that says why it cannot be used.
  const renderImageOption = (image: (typeof images)[number]) => {
    const blocker = installBlocker(image)
    return (
      <option key={image.id} value={image.id} disabled={blocker !== null}>
        {image.title} {image.version ? `(${image.version})` : ''}
        {image.kind && image.kind !== 'os' ? ` — ${image.kind}` : ''}
        {image.id === defaultImageId
          ? ` — ${t('widgets.sidebars.installOS.defaultImage')}`
          : ''}
        {blocker
          ? ` — ${t(`widgets.sidebars.installOS.unavailable.${blocker}`)}`
          : ''}
      </option>
    )
  }

  return (
    <div className="flex flex-col gap-6">
      <SubViewHeader title={headingText} onBack={handleBack} />

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <form onSubmit={handleFormSubmit} className="flex flex-col gap-6">
        {/* OS Image Selection */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.installOS.imageLabel')}
          </label>
          <select
            value={selectedImageId}
            onChange={(e) => setSelectedImageId(e.target.value)}
            className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
            required
          >
            <option value="">
              {t('widgets.sidebars.installOS.selectImage')}
            </option>
            {osImages.length > 0 && (
              <optgroup label={t('widgets.sidebars.installOS.osImagesGroup')}>
                {osImages.map(renderImageOption)}
              </optgroup>
            )}
            {captureImages.length > 0 && (
              <optgroup
                label={t('widgets.sidebars.installOS.captureImagesGroup')}
              >
                {captureImages.map(renderImageOption)}
              </optgroup>
            )}
          </select>
        </div>

        {/* Storage Device Selection */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.installOS.targetDeviceLabel')}
          </label>
          {deviceEntries.length === 0 ? (
            <div className="text-sm text-neutral-7 px-3 py-2 bg-neutral-13 border border-neutral-11 rounded">
              {t('widgets.sidebars.installOS.noStorageDevices')}
            </div>
          ) : (
            <select
              value={selectedDevice}
              onChange={(e) => setSelectedDevice(e.target.value)}
              className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
              required
            >
              <option value="">
                {t('widgets.sidebars.installOS.selectStorageDevice')}
              </option>
              {deviceEntries.map(([devicePath, deviceInfo]) => {
                const displayLabel = deviceInfo.label || devicePath
                const capacityGB = deviceInfo.total_capacity
                  ? (deviceInfo.total_capacity / (1024 * 1024 * 1024)).toFixed(
                      1,
                    )
                  : ''
                const label = capacityGB
                  ? `${devicePath} - ${displayLabel} (${capacityGB} GB)`
                  : `${devicePath} - ${displayLabel}`
                return (
                  <option key={devicePath} value={devicePath}>
                    {label}
                  </option>
                )
              })}
            </select>
          )}
        </div>

        {/* No "reboot after installation": a freshly imaged disk has no agent
            or credentials yet, so rebooting into it would strand the device. It
            stays netbooted; reboot explicitly once provisioned. */}

        {/* Submit Button */}
        <Button
          type="submit"
          size="lg"
          disabled={
            isProcessing ||
            !selectedImageId ||
            !selectedDevice ||
            deviceEntries.length === 0
          }
          grow
        >
          {isProcessing
            ? t('widgets.sidebars.installOS.installing')
            : headingText}
        </Button>
      </form>

      {/* Names the device and says what happens to it. The operator picked the
          target from a dropdown two fields up; the point of repeating it here is
          that this is the last moment before the disk is overwritten. */}
      <ConfirmDialog
        open={confirmOpen}
        title={headingText}
        destructive
        confirmText={headingText}
        busy={isProcessing}
        onCancel={() => setConfirmOpen(false)}
        onConfirm={runInstall}
        message={
          <span>
            {t('widgets.sidebars.installOS.confirmErases')}{' '}
            <span className="text-neutral-1">{selectedDevice}</span>{' '}
            {t('widgets.sidebars.installOS.confirmOn')}{' '}
            <span className="text-neutral-1">
              {node?.name || t('widgets.sidebars.installOS.thisNode')}
            </span>{' '}
            {t('widgets.sidebars.installOS.confirmWrites')}{' '}
            <span className="text-neutral-1">
              {images.find((i) => i.id === selectedImageId)?.title ||
                t('widgets.sidebars.installOS.selectedImageFallback')}
            </span>{' '}
            {t('widgets.sidebars.installOS.confirmOverIt')}
          </span>
        }
      />
    </div>
  )
}
