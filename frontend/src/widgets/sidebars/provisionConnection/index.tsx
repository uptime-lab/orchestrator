import { useNavigate } from '@tanstack/react-router'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SubViewHeader } from '@/widgets/sidebars/node/subViewHeader'
import { SidebarEnum } from '@/widgets/sidebars/types'

import { apiAccess } from '@/entities/access'
import { EffectiveAccessEntry } from '@/entities/access/api/types'
import { nodesModel } from '@/entities/node'
import { useNode } from '@/entities/node/hooks.ts'

import { Button } from '@/shared/ui/button/button.tsx'

import { SidebarProps } from '../types'

// Recovery-mode access provisioning. This panel used to be the
// legacy single-user provision_connection form (a username + one SSH key from
// OU policy). It now shows what will ACTUALLY be written — the node's resolved
// effective access (the union of access entries across node → OU → root →
// account) — and writes exactly that onto the target disk while the node is
// netbooted. There is no username field on purpose: the access entries in
// Credentials are the single source of truth for who may log in.
export const SidebarProvisionConnection: FC<
  SidebarProps & { nodeId: string; onDone?: () => void }
> = ({ nodeId, onDone }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { node } = useNode({ nodeId })
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [applied, setApplied] = useState(false)
  const [selectedDevice, setSelectedDevice] = useState<string>('')
  const [rootPartition, setRootPartition] = useState<string>('')
  const [entries, setEntries] = useState<EffectiveAccessEntry[] | null>(null)
  const [accessError, setAccessError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    apiAccess
      .getEffectiveAccess(nodeId)
      .then((eff) => {
        if (!cancelled) setEntries(eff)
      })
      .catch(() => {
        if (!cancelled)
          setAccessError(
            t('widgets.sidebars.provisionConnection.accessLoadError'),
          )
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, t])

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

  const users = (entries ?? []).filter((e) => e.type === 'user')
  const cas = (entries ?? []).filter((e) => e.type === 'ca')
  // The SERVER derives each user's lifecycle (active if it has any credential
  // — a key, a trusted-CA principal, OR a password — locked otherwise) and it
  // is the source of truth for the lockout gate. The panel must not recompute
  // it from the key count alone: a user with a password and no key is active,
  // and showing it LOCKED (as an earlier version did) contradicts the very
  // config the platform will write.
  const grantsLogin = users.some((u) => u.lifecycle === 'active')

  const handleFormSubmit = useCallback(
    async (event: React.FormEvent<HTMLFormElement>) => {
      event.preventDefault()
      setFormError(null)

      if (!selectedDevice) {
        setFormError(
          t('widgets.sidebars.provisionConnection.selectTargetDeviceError'),
        )
        return
      }

      setIsProcessing(true)
      try {
        await apiAccess.applyOfflineNodeAccess(nodeId, {
          target_device: selectedDevice,
          root_partition: rootPartition
            ? parseInt(rootPartition, 10)
            : undefined,
        })
        await nodesModel.fetchNodesFx()
        setApplied(true)
      } catch (error) {
        const err = error as {
          response?: { data?: { message?: string; error?: string } }
          message?: string
        }
        const errorMessage =
          err?.response?.data?.message ||
          err?.response?.data?.error ||
          err?.message ||
          t('widgets.sidebars.provisionConnection.writeFailed')
        setFormError(errorMessage)
      } finally {
        setIsProcessing(false)
      }
    },
    [selectedDevice, rootPartition, nodeId, t],
  )

  const storageDevices = node?.storage_devices || {}
  const deviceEntries = Object.entries(storageDevices)
  const sourceLabel = (e: EffectiveAccessEntry) =>
    e.source.level === 'node'
      ? t('widgets.sidebars.provisionConnection.sourceNode')
      : e.source.level === 'account'
        ? t('widgets.sidebars.provisionConnection.sourceAccount')
        : e.source.level === 'root'
          ? t('widgets.sidebars.provisionConnection.sourceRootOU')
          : t('widgets.sidebars.provisionConnection.sourceOU')

  return (
    <div className="flex flex-col gap-6">
      <SubViewHeader
        title={t('widgets.sidebars.provisionConnection.title')}
        onBack={handleBack}
      />

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}
      {applied && (
        <div className="bg-green-6/10 border border-green-6/50 text-green-6 px-4 py-2 rounded text-sm">
          {t('widgets.sidebars.provisionConnection.appliedNotice')}
        </div>
      )}

      {/* What will be written: the node's resolved effective access. */}
      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.provisionConnection.accessLabel')}
        </label>
        {accessError ? (
          <div className="text-sm text-red-6 px-3 py-2 bg-neutral-13 border border-neutral-11 rounded">
            {accessError}
          </div>
        ) : entries === null ? (
          <div className="text-sm text-neutral-7 px-3 py-2 bg-neutral-13 border border-neutral-11 rounded">
            {t('widgets.sidebars.provisionConnection.loadingAccess')}
          </div>
        ) : users.length === 0 ? (
          <div className="text-sm text-neutral-7 px-3 py-2 bg-neutral-13 border border-neutral-11 rounded">
            {t('widgets.sidebars.provisionConnection.noAccessEntries')}
          </div>
        ) : (
          <div className="flex flex-col gap-1">
            {users.map((u) => (
              <div
                key={u.entry_id}
                className="flex items-center justify-between px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm"
              >
                <div className="flex items-center gap-2 min-w-0">
                  <span className="font-mono text-neutral-2 truncate">
                    {u.username}
                  </span>
                  {u.sudo && (
                    <span
                      className="text-xxs uppercase tracking-wide rounded border border-purple-5 text-purple-5 px-1"
                      title={
                        u.has_password
                          ? t(
                              'widgets.sidebars.provisionConnection.sudoWithPasswordTitle',
                            )
                          : t(
                              'widgets.sidebars.provisionConnection.sudoNopasswdTitle',
                            )
                      }
                    >
                      sudo{u.has_password ? '' : ' · nopasswd'}
                    </span>
                  )}
                  {u.lifecycle === 'locked' && (
                    <span
                      className="text-xxs uppercase tracking-wide rounded border border-red-6 text-red-6 px-1"
                      title={t(
                        'widgets.sidebars.provisionConnection.lockedTitle',
                      )}
                    >
                      {t('widgets.sidebars.provisionConnection.locked')}
                    </span>
                  )}
                </div>
                <div className="flex items-center gap-3 flex-shrink-0 text-neutral-6 text-xs">
                  <span>
                    {t('widgets.sidebars.provisionConnection.keysCount', {
                      count: u.key_ids.length,
                    })}
                    {(u.principals?.length ?? 0) > 0 &&
                      ` · ${t('widgets.sidebars.provisionConnection.principalsCount', { count: u.principals!.length })}`}
                    {u.has_password &&
                      ` · ${t('widgets.sidebars.provisionConnection.password')}`}
                  </span>
                  <span
                    title={t(
                      'widgets.sidebars.provisionConnection.sourceTitle',
                    )}
                  >
                    {sourceLabel(u)}
                  </span>
                </div>
              </div>
            ))}
            {cas.length > 0 && (
              <div className="text-xs text-neutral-6 px-3 py-1">
                {t('widgets.sidebars.provisionConnection.caCount', {
                  count: cas.length,
                })}
              </div>
            )}
          </div>
        )}
        <p className="text-xs text-neutral-7">
          {t('widgets.sidebars.provisionConnection.accessHint')}
        </p>
      </div>

      <form onSubmit={handleFormSubmit} className="flex flex-col gap-6">
        {/* Target device */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.provisionConnection.targetDeviceLabel')}
          </label>
          {deviceEntries.length === 0 ? (
            <div className="text-sm text-neutral-7 px-3 py-2 bg-neutral-13 border border-neutral-11 rounded">
              {t('widgets.sidebars.provisionConnection.noStorageDevices')}
            </div>
          ) : (
            <select
              value={selectedDevice}
              onChange={(e) => setSelectedDevice(e.target.value)}
              className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
              required
            >
              <option value="">
                {t('widgets.sidebars.provisionConnection.selectStorageDevice')}
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

        {/* Root partition (optional) */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.provisionConnection.rootPartitionLabel')}
          </label>
          <input
            type="number"
            min="0"
            value={rootPartition}
            onChange={(e) => setRootPartition(e.target.value)}
            placeholder={t(
              'widgets.sidebars.provisionConnection.rootPartitionPlaceholder',
            )}
            className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
          />
          <p className="text-xs text-neutral-7">
            {t('widgets.sidebars.provisionConnection.rootPartitionHint')}
          </p>
        </div>

        <Button
          type="submit"
          disabled={
            isProcessing || !selectedDevice || entries === null || !grantsLogin
          }
          title={
            !grantsLogin && entries !== null
              ? t('widgets.sidebars.provisionConnection.noLoginTitle')
              : undefined
          }
        >
          {isProcessing
            ? t('widgets.sidebars.provisionConnection.writing')
            : t('widgets.sidebars.provisionConnection.submit')}
        </Button>
        <p className="text-xs text-neutral-7">
          {t('widgets.sidebars.provisionConnection.footerNote')}
        </p>
      </form>
    </div>
  )
}
