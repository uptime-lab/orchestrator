import { FC, useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import type { StorageDevice } from '@/entities/node/api/types'

import { Button } from '@/shared/ui/button/button.tsx'

import { InfoRow, SectionDivider } from './infoRow.tsx'
import { deviceTypeLabel } from './overviewData.ts'
import { SubViewHeader } from './subViewHeader.tsx'

const GB = 1024 * 1024 * 1024
const sizeGB = (bytes?: number): string | null =>
  bytes && bytes > 0 ? `${(bytes / GB).toFixed(1)} GB` : null

// StorageDeviceSection is the per-drive view opened from Advanced → Storage.
// It lives inside the node panel like every other sub-view (it used to be a
// separate sidebar with its own node header and actions menu, which lost the
// tabs and duplicated node-level actions with a different permission scope).
export const StorageDeviceSection: FC<{
  nodeId: string
  devicePath: string
  device: StorageDevice
  canErase: boolean
  onBack: () => void
}> = ({ nodeId, devicePath, device, canErase, onBack }) => {
  const { t } = useTranslation()
  const [confirming, setConfirming] = useState(false)
  const [confirmDevicePath, setConfirmDevicePath] = useState('')
  const [isErasing, setIsErasing] = useState(false)
  const [eraseError, setEraseError] = useState<string | null>(null)

  // The drive holding the root filesystem is never erasable — the agent runs
  // from it. Detected from the partitions the node reported.
  const isBootDrive =
    device.partitions?.some((p) => p.mount_point === '/') || false

  const totalUsed =
    device.partitions?.reduce((sum, p) => sum + (p.used_space || 0), 0) || 0
  const totalAvailable =
    device.partitions?.reduce((sum, p) => sum + (p.available_space || 0), 0) ||
    0

  const handleConfirmErase = useCallback(async () => {
    if (confirmDevicePath !== devicePath) return
    try {
      setIsErasing(true)
      setEraseError(null)
      await apiNodes.postEraseDrive(nodeId, devicePath)
      setConfirming(false)
      setConfirmDevicePath('')
      await nodesModel.fetchNodesFx()
    } catch (error: unknown) {
      const err = error as {
        response?: { data?: { message?: string; error?: string } }
        message?: string
      }
      setEraseError(
        err?.response?.data?.message ||
          err?.response?.data?.error ||
          err?.message ||
          t('widgets.sidebars.node.storageDeviceSection.eraseError'),
      )
    } finally {
      setIsErasing(false)
    }
  }, [nodeId, devicePath, confirmDevicePath, t])

  // Erase confirmation is a nested step of this sub-view (type the device path
  // to arm it), not a modal — same slide-in shape as the Actions confirmations.
  if (confirming) {
    return (
      <div className="flex flex-col gap-4">
        <SubViewHeader
          title={t('widgets.sidebars.node.storageDeviceSection.eraseTitle')}
          onBack={() => {
            setConfirming(false)
            setConfirmDevicePath('')
            setEraseError(null)
          }}
        />
        <div className="text-sm text-neutral-4">
          {t('widgets.sidebars.node.storageDeviceSection.eraseWarning')}
        </div>
        <div className="rounded border border-neutral-12 bg-neutral-13 px-3 py-2 font-mono text-sm text-neutral-3">
          {devicePath}
        </div>
        <input
          type="text"
          value={confirmDevicePath}
          onChange={(e) => {
            setConfirmDevicePath(e.target.value)
            setEraseError(null)
          }}
          placeholder={devicePath}
          disabled={isErasing}
          autoFocus
          className="rounded border border-neutral-12 bg-neutral-15 px-3 py-2 font-mono text-sm text-neutral-2 focus:border-red-6 focus:outline-none disabled:opacity-50"
        />
        {eraseError && (
          <div className="rounded border border-red-6/50 bg-red-6/10 px-3 py-2 text-sm text-red-6">
            {eraseError}
          </div>
        )}
        <div className="flex gap-2">
          <Button
            variant="destructive"
            onClick={handleConfirmErase}
            disabled={isErasing || confirmDevicePath !== devicePath}
            grow
          >
            {isErasing
              ? t('widgets.sidebars.node.storageDeviceSection.erasing')
              : t('widgets.sidebars.node.storageDeviceSection.eraseDrive')}
          </Button>
          <Button
            variant="normal"
            kind="secondary"
            onClick={() => {
              setConfirming(false)
              setConfirmDevicePath('')
              setEraseError(null)
            }}
            disabled={isErasing}
          >
            {t('widgets.sidebars.node.storageDeviceSection.cancel')}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <SubViewHeader title={devicePath} onBack={onBack} />

      <div className="flex flex-col gap-2">
        <InfoRow label={t('widgets.sidebars.node.storageDeviceSection.device')}>
          <span className="font-mono">{devicePath}</span>
        </InfoRow>
        <InfoRow
          label={t('widgets.sidebars.node.storageDeviceSection.capacity')}
        >
          {sizeGB(device.total_capacity) ||
            t('widgets.sidebars.node.storageDeviceSection.na')}
        </InfoRow>
        {(totalUsed > 0 || totalAvailable > 0) && (
          <InfoRow label={t('widgets.sidebars.node.storageDeviceSection.used')}>
            {[
              sizeGB(totalUsed)
                ? t('widgets.sidebars.node.storageDeviceSection.usedAmount', {
                    size: sizeGB(totalUsed),
                  })
                : null,
              sizeGB(totalAvailable)
                ? t('widgets.sidebars.node.storageDeviceSection.freeAmount', {
                    size: sizeGB(totalAvailable),
                  })
                : null,
            ]
              .filter(Boolean)
              .join(' · ')}
          </InfoRow>
        )}
        {device.model && (
          <InfoRow
            label={t('widgets.sidebars.node.storageDeviceSection.model')}
          >
            <span className="block truncate">{device.model}</span>
          </InfoRow>
        )}
        {device.vendor && (
          <InfoRow
            label={t('widgets.sidebars.node.storageDeviceSection.vendor')}
          >
            {device.vendor}
          </InfoRow>
        )}
        {device.device_type && (
          <InfoRow label={t('widgets.sidebars.node.storageDeviceSection.type')}>
            {deviceTypeLabel(device.device_type)}
          </InfoRow>
        )}
        {device.serial && (
          <InfoRow
            label={t('widgets.sidebars.node.storageDeviceSection.serial')}
          >
            <span className="block truncate font-mono">{device.serial}</span>
          </InfoRow>
        )}
        <InfoRow
          label={t('widgets.sidebars.node.storageDeviceSection.removable')}
        >
          {device.is_removable
            ? t('widgets.sidebars.node.storageDeviceSection.yes')
            : t('widgets.sidebars.node.storageDeviceSection.no')}
        </InfoRow>
        <InfoRow
          label={t('widgets.sidebars.node.storageDeviceSection.readOnly')}
        >
          {device.is_read_only
            ? t('widgets.sidebars.node.storageDeviceSection.yes')
            : t('widgets.sidebars.node.storageDeviceSection.no')}
        </InfoRow>
      </div>

      {device.partitions && device.partitions.length > 0 && (
        <div className="flex flex-col gap-2">
          <SectionDivider
            label={t('widgets.sidebars.node.storageDeviceSection.partitions')}
          />
          {device.partitions.map((partition) => (
            <div
              key={partition.partition_id}
              className="rounded border border-neutral-12 bg-neutral-13 p-3"
            >
              <div className="mb-2 font-mono text-sm text-neutral-3">
                {partition.partition_id}
              </div>
              <div className="flex flex-col gap-1 text-xs text-neutral-6">
                {partition.mount_point && (
                  <div>
                    <span className="text-neutral-7">
                      {t(
                        'widgets.sidebars.node.storageDeviceSection.mountPoint',
                      )}
                    </span>{' '}
                    {partition.mount_point}
                  </div>
                )}
                {partition.filesystem && (
                  <div>
                    <span className="text-neutral-7">
                      {t(
                        'widgets.sidebars.node.storageDeviceSection.filesystem',
                      )}
                    </span>{' '}
                    {partition.filesystem}
                  </div>
                )}
                {(partition.used_space > 0 ||
                  partition.available_space > 0) && (
                  <div>
                    <span className="text-neutral-7">
                      {t(
                        'widgets.sidebars.node.storageDeviceSection.usedColon',
                      )}
                    </span>{' '}
                    {sizeGB(partition.used_space) ||
                      t('widgets.sidebars.node.storageDeviceSection.na')}
                    ,{' '}
                    <span className="text-neutral-7">
                      {t(
                        'widgets.sidebars.node.storageDeviceSection.availableColon',
                      )}
                    </span>{' '}
                    {sizeGB(partition.available_space) ||
                      t('widgets.sidebars.node.storageDeviceSection.na')}
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {canErase && (
        <div className="flex flex-col gap-2">
          <SectionDivider
            label={t('widgets.sidebars.node.storageDeviceSection.danger')}
            tone="danger"
          />
          <Button
            variant="destructive"
            onClick={() => {
              setEraseError(null)
              setConfirming(true)
            }}
            disabled={isBootDrive}
            title={
              isBootDrive
                ? t('widgets.sidebars.node.storageDeviceSection.bootDriveTitle')
                : t(
                    'widgets.sidebars.node.storageDeviceSection.eraseButtonTitle',
                  )
            }
            grow
          >
            {t('widgets.sidebars.node.storageDeviceSection.eraseDrive')}
          </Button>
          {isBootDrive && (
            <div className="text-xs text-neutral-6">
              {t('widgets.sidebars.node.storageDeviceSection.bootDriveNote')}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
