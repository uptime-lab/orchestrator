import { useNavigate } from '@tanstack/react-router'
import { FC, useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SubViewHeader } from '@/widgets/sidebars/node/subViewHeader'
import { SidebarEnum } from '@/widgets/sidebars/types'

import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import { useNode } from '@/entities/node/hooks.ts'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarProps } from '../types'
import { buildCaptureParams, defaultCaptureTarget } from './captureTarget'

// SidebarCaptureImage triggers a capture_image command: it snapshots the node's
// disk into a new platform image. A byte-exact backup (default) restores onto
// this same node with Install OS; a generalized (golden) capture strips machine
// identity so the image deploys onto any device. Capture must run from netboot
// recovery, so the entry point (node Actions → Provisioning) is recovery-only.
export const SidebarCaptureImage: FC<
  SidebarProps & { nodeId: string; onDone?: () => void }
> = ({ nodeId, onDone }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { node } = useNode({ nodeId })
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [title, setTitle] = useState('')
  const [generalize, setGeneralize] = useState(false)
  const storageDevices =
    node?.system_info?.storage_devices || node?.storage_devices || {}
  const deviceEntries = Object.entries(storageDevices)
  const rootDevice = node?.system_info?.hardware?.boot?.root_device
  const [chosenDevice, setChosenDevice] = useState<string | null>(null)
  const targetDevice =
    chosenDevice ?? defaultCaptureTarget(storageDevices, rootDevice)

  const handleBack = useCallback(() => {
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
        view: 'actions',
      }),
    })
  }, [navigate, nodeId, onDone])

  const handleSubmit = useCallback(
    async (event: React.FormEvent<HTMLFormElement>) => {
      event.preventDefault()
      setFormError(null)

      if (!title.trim()) {
        setFormError(t('widgets.sidebars.captureImage.titleRequired'))
        return
      }

      const params = buildCaptureParams(title, generalize, targetDevice)
      if (!params) {
        setFormError(t('widgets.sidebars.captureImage.diskRequired'))
        return
      }

      setIsProcessing(true)
      try {
        await apiNodes.postNodeCommand(nodeId, {
          command: 'capture_image',
          params,
        })
        await nodesModel.fetchNodesFx()
        handleBack()
      } catch (error) {
        setFormError(
          extractApiErrorMessage(
            error,
            t('widgets.sidebars.captureImage.captureFailed'),
          ),
        )
      } finally {
        setIsProcessing(false)
      }
    },
    [title, generalize, targetDevice, nodeId, handleBack, t],
  )

  return (
    <div className="flex flex-col gap-6">
      <SubViewHeader
        title={t('widgets.sidebars.captureImage.title')}
        onBack={handleBack}
      />

      <div className="text-sm text-neutral-6">
        {t('widgets.sidebars.captureImage.intro')}
      </div>

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <form onSubmit={handleSubmit} className="flex flex-col gap-6">
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.captureImage.imageTitleLabel')}
          </label>
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={t(
              'widgets.sidebars.captureImage.imageTitlePlaceholder',
            )}
            spellCheck={false}
            className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
            required
          />
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.captureImage.targetDeviceLabel')}
          </label>
          {deviceEntries.length === 0 ? (
            <div className="text-sm text-neutral-7 px-3 py-2 bg-neutral-13 border border-neutral-11 rounded">
              {t('widgets.sidebars.captureImage.noStorageDevices')}
            </div>
          ) : (
            <select
              value={targetDevice}
              onChange={(e) => setChosenDevice(e.target.value)}
              className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
            >
              <option value="" disabled>
                {t('widgets.sidebars.captureImage.chooseDisk')}
              </option>
              {deviceEntries.map(([devicePath, deviceInfo]) => {
                const displayLabel = deviceInfo.label || devicePath
                const capacityGB = deviceInfo.total_capacity
                  ? (deviceInfo.total_capacity / (1024 * 1024 * 1024)).toFixed(
                      1,
                    )
                  : ''
                const base = capacityGB
                  ? `${devicePath} - ${displayLabel} (${capacityGB} GB)`
                  : `${devicePath} - ${displayLabel}`
                const label =
                  devicePath === rootDevice
                    ? t('widgets.sidebars.captureImage.bootDiskOption', {
                        device: base,
                      })
                    : base
                return (
                  <option key={devicePath} value={devicePath}>
                    {label}
                  </option>
                )
              })}
            </select>
          )}
        </div>

        <div className="flex items-start gap-2">
          <input
            type="checkbox"
            id="generalize"
            checked={generalize}
            onChange={(e) => setGeneralize(e.target.checked)}
            className="mt-0.5 w-4 h-4 text-purple-5 bg-neutral-13 border-neutral-11 rounded focus:ring-purple-5"
          />
          <label htmlFor="generalize" className="text-sm text-neutral-4">
            {t('widgets.sidebars.captureImage.generalizeLabel')}
          </label>
        </div>

        <Button
          type="submit"
          size="lg"
          disabled={isProcessing || !title.trim() || !targetDevice}
          grow
        >
          {isProcessing
            ? t('widgets.sidebars.captureImage.startingCapture')
            : t('widgets.sidebars.captureImage.submit')}
        </Button>
      </form>
    </div>
  )
}
