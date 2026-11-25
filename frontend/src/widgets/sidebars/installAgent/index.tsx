import { useNavigate } from '@tanstack/react-router'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SubViewHeader } from '@/widgets/sidebars/node/subViewHeader'
import { SidebarEnum } from '@/widgets/sidebars/types'

import { nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import { useNode } from '@/entities/node/hooks.ts'
import {
  bootOrderCanReturnToNetboot,
  nodeDidNetbootIntoRecovery,
} from '@/entities/node/utils'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarProps } from '../types'

export const SidebarInstallAgent: FC<
  SidebarProps & { nodeId: string; onDone?: () => void }
> = ({ nodeId, onDone }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { node } = useNode({ nodeId })
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [selectedDevice, setSelectedDevice] = useState<string>('')
  const [rootPartition, setRootPartition] = useState<string>('')
  const [agentVersion, setAgentVersion] = useState<string>('')
  const [variant, setVariant] = useState<'full' | 'watcher'>('full')
  const [availableVersions, setAvailableVersions] = useState<string[]>([])

  // Load the agent versions actually available in the CDN so the operator picks a
  // real one (a free-text field previously let them pick a non-existent version).
  useEffect(() => {
    let cancelled = false
    fetch('/cdn/agent-versions')
      .then((r) => (r.ok ? r.json() : { versions: [] }))
      .then((data) => {
        if (cancelled) return
        const versions: string[] = data?.versions?.length
          ? data.versions
          : ['latest']
        setAvailableVersions(versions)
        setAgentVersion((prev) => prev || versions[0])
      })
      .catch(() => {
        if (cancelled) return
        setAvailableVersions(['latest'])
        setAgentVersion((prev) => prev || 'latest')
      })
    return () => {
      cancelled = true
    }
  }, [])

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
      }),
    })
  }, [navigate, nodeId, onDone])

  const handleFormSubmit = useCallback(
    async (event: React.FormEvent<HTMLFormElement>) => {
      event.preventDefault()
      setFormError(null)

      if (!selectedDevice) {
        setFormError(t('widgets.sidebars.installAgent.selectTargetDeviceError'))
        return
      }

      if (!agentVersion) {
        setFormError(t('widgets.sidebars.installAgent.enterAgentVersionError'))
        return
      }

      setIsProcessing(true)

      try {
        await apiNodes.postInstallAgent(nodeId, {
          target_device: selectedDevice,
          root_partition: rootPartition
            ? parseInt(rootPartition, 10)
            : undefined,
          agent_version: agentVersion,
          variant,
        })

        // Refresh nodes to get updated status
        await nodesModel.fetchNodesFx()

        // Navigate back to node sidebar
        handleBack()
      } catch (error: unknown) {
        setFormError(
          extractApiErrorMessage(
            error,
            t('widgets.sidebars.installAgent.installFailed'),
          ),
        )
      } finally {
        setIsProcessing(false)
      }
    },
    [
      selectedDevice,
      rootPartition,
      agentVersion,
      variant,
      nodeId,
      handleBack,
      t,
    ],
  )

  // Get storage devices from node
  const storageDevices = node?.storage_devices || {}
  const deviceEntries = Object.entries(storageDevices)

  // Installing Watcher is the one action here that cannot be undone from the
  // platform: the node stops executing commands, so the only way back is a
  // recovery boot the DEVICE has to initiate. The same rule the server enforces
  // is evaluated here so the operator is told before committing, not after.
  // BOTH halves of the server rule, or the panel fails at its stated job in the
  // most common case: Install Agent is offered on any node, so a live-OS node
  // with a perfect boot order would have had an enabled button and a 409.
  const netbootReturn = bootOrderCanReturnToNetboot(
    node?.system_info?.hardware?.boot?.boot_devices_order,
  )
  const watcherReason = !nodeDidNetbootIntoRecovery(node)
    ? t('widgets.sidebars.installAgent.watcherReasonNotNetbooted')
    : !netbootReturn.ok
      ? netbootReturn.reason
      : ''
  const watcherBlocked = variant === 'watcher' && watcherReason !== ''

  return (
    <div className="flex flex-col gap-6">
      <SubViewHeader
        title={t('widgets.sidebars.installAgent.title')}
        onBack={handleBack}
      />

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <form onSubmit={handleFormSubmit} className="flex flex-col gap-6">
        {/* Storage Device Selection */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.installAgent.targetDeviceLabel')}
          </label>
          {deviceEntries.length === 0 ? (
            <div className="text-sm text-neutral-7 px-3 py-2 bg-neutral-13 border border-neutral-11 rounded">
              {t('widgets.sidebars.installAgent.noStorageDevices')}
            </div>
          ) : (
            <select
              value={selectedDevice}
              onChange={(e) => setSelectedDevice(e.target.value)}
              className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
              required
            >
              <option value="">
                {t('widgets.sidebars.installAgent.selectStorageDevice')}
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

        {/* Root Partition Selection (Optional) */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.installAgent.rootPartitionLabel')}
          </label>
          <input
            type="number"
            min="0"
            value={rootPartition}
            onChange={(e) => setRootPartition(e.target.value)}
            placeholder={t(
              'widgets.sidebars.installAgent.rootPartitionPlaceholder',
            )}
            className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
          />
          <p className="text-xs text-neutral-7">
            {t('widgets.sidebars.installAgent.rootPartitionHint')}
          </p>
        </div>

        {/* Agent Type */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.installAgent.agentTypeLabel')}
          </label>
          <select
            value={variant}
            onChange={(e) => setVariant(e.target.value as 'full' | 'watcher')}
            className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
          >
            <option value="full">
              {t('widgets.sidebars.installAgent.variantFull')}
            </option>
            <option value="watcher">
              {t('widgets.sidebars.installAgent.variantWatcher')}
            </option>
          </select>
          {variant === 'watcher' && (
            <p className="text-xs text-neutral-7">
              {t('widgets.sidebars.installAgent.watcherNote')}
            </p>
          )}
          {watcherBlocked && (
            <div className="bg-yellow-6/10 border border-yellow-6/50 text-yellow-5 px-3 py-2 rounded text-xs flex flex-col gap-1">
              <span className="font-medium">
                {t('widgets.sidebars.installAgent.watcherBlockedTitle')}
              </span>
              <span>
                {t('widgets.sidebars.installAgent.watcherBlockedReason', {
                  reason: watcherReason,
                })}
              </span>
              <span>
                {t('widgets.sidebars.installAgent.watcherBlockedRemedy')}
              </span>
            </div>
          )}
        </div>

        {/* Agent Version */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.installAgent.agentVersionLabel')}
          </label>
          <select
            value={agentVersion}
            onChange={(e) => setAgentVersion(e.target.value)}
            className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
            required
          >
            {availableVersions.length === 0 && (
              <option value="">
                {t('widgets.sidebars.installAgent.loadingVersions')}
              </option>
            )}
            {availableVersions.map((v) => (
              <option key={v} value={v}>
                {v}
              </option>
            ))}
          </select>
          <p className="text-xs text-neutral-7">
            {t('widgets.sidebars.installAgent.agentVersionHint')}
          </p>
        </div>

        {/* Submit Button */}
        <Button
          type="submit"
          size="lg"
          disabled={
            isProcessing ||
            !selectedDevice ||
            !agentVersion ||
            deviceEntries.length === 0 ||
            watcherBlocked
          }
          grow
        >
          {isProcessing
            ? t('widgets.sidebars.installAgent.installing')
            : t('widgets.sidebars.installAgent.submit')}
        </Button>
      </form>
    </div>
  )
}
