import { useNavigate } from '@tanstack/react-router'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SidebarEnum } from '@/widgets/sidebars/types'

import {
  accessChanged,
  apiAccess,
  caPlacements,
  keyDeleteImpact,
  withUnitRemoved,
  type AccessEntry,
  type CAPlacement,
} from '@/entities/access'
import { useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { apiSSHKeys, sshKeysModel, useSSHKey } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showError } from '@/shared/ui/toast'

import { SidebarCloseButton } from '../closeButton'
import { SidebarProps } from '../types'

export const SidebarSSHKeyDetails: FC<SidebarProps & { keyId: string }> = ({
  keyId,
  onClose,
}) => {
  const navigate = useNavigate()
  const { t } = useTranslation()
  const { ous } = useOUs()
  const { key } = useSSHKey({ keyId })
  const [isProcessing, setIsProcessing] = useState(false)
  const [isDeleteConfirmOpen, setIsDeleteConfirmOpen] = useState(false)
  // Who loses the key, counted when the delete is asked for; null while
  // unknown (or for a role that cannot list device login users).
  const [deleteImpact, setDeleteImpact] = useState<{
    users: number
    locked: number
  } | null>(null)
  // CA trust entries referencing this key (for the "Trusted on" section).
  const [caTrusts, setCaTrusts] = useState<AccessEntry[]>([])

  useEffect(() => {
    // Fetch the key if not in store
    if (!key) {
      sshKeysModel.fetchSSHKeyFx(keyId)
    }
  }, [keyId, key])

  const isCA = key?.key_type === 'ca'
  // Where a CA is trusted is read from the access entries, which are served
  // on access.manage; without it the section is not shown.
  const canReadTrusts = useCan()(Actions.AccessManage)
  const canManageKeys = useCan()(Actions.SSHKeyManage)

  const openDeleteConfirm = useCallback(async () => {
    setDeleteImpact(null)
    setIsDeleteConfirmOpen(true)
    if (isCA || !canReadTrusts) return
    try {
      const all = await apiAccess.getAccessEntries({ kind: 'all' })
      setDeleteImpact(keyDeleteImpact(all, keyId))
    } catch {
      // The general wording still stands; the counts are an addition.
    }
  }, [isCA, canReadTrusts, keyId])

  const loadTrusts = useCallback(async () => {
    if (!isCA || !canReadTrusts) return
    try {
      const all = await apiAccess.getAccessEntries({ kind: 'all' })
      setCaTrusts(
        all.filter((e) => e.type === 'ca' && e.key_ids.includes(keyId)),
      )
    } catch {
      // Non-fatal: the section just shows nothing.
    }
  }, [isCA, canReadTrusts, keyId])

  useEffect(() => {
    void loadTrusts()
  }, [loadTrusts])
  useEffect(() => {
    const sub = accessChanged.watch(() => void loadTrusts())
    return () => sub.unsubscribe()
  }, [loadTrusts])

  // Deduplicated placements: one chip per unit + one "All nodes" chip.
  const placements = caPlacements(caTrusts, ous, {
    accountWide: t('pages.credentials.accountWide'),
    node: t('pages.credentials.trustedCA.nodeScope'),
  })

  // Each entry is re-read before its units are rewritten, so a unit added
  // elsewhere since this panel loaded is kept.
  const removePlacement = async (p: CAPlacement) => {
    setIsProcessing(true)
    try {
      for (const e of caTrusts) {
        if (p.accountWide && e.scope === 'account') {
          await apiAccess.deleteAccessEntry(e.id)
        } else if (p.ouId && e.ou_ids.includes(p.ouId)) {
          const ouId = p.ouId
          const current = await apiAccess.getAccessEntry(e.id)
          const edit = withUnitRemoved(current, ouId)
          if (!edit) continue
          if (edit.scope?.ou_ids?.length === 0)
            await apiAccess.deleteAccessEntry(e.id)
          else await apiAccess.updateAccessEntry(e.id, edit)
        }
      }
    } catch (err) {
      showError(t('pages.credentials.trustedCA.removeError'), err)
    } finally {
      accessChanged()
      setIsProcessing(false)
    }
  }

  const handleEdit = useCallback(() => {
    navigate({
      to: '.',
      // Merge, don't replace: dropping `tab` here bounced a CA key's edit
      // onto the ssh-keys tab (the route's default).
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        sidebar: SidebarEnum.EditSSHKey,
        keyId,
      }),
    })
  }, [navigate, keyId])

  const handleDelete = useCallback(async () => {
    setIsDeleteConfirmOpen(false)
    onClose()
    try {
      setIsProcessing(true)
      await apiSSHKeys.deleteSSHKey(keyId)
      await sshKeysModel.fetchSSHKeysFx()
      accessChanged()
    } catch (error) {
      // This is that follow-up. "The list not changing" is not a signal a user
      // can read — the panel has already closed, so an unchanged list looks
      // identical to a slow refresh, and a key still granting access looks
      // deleted. The server's reason (an access entry still references it) is
      // exactly what the operator needs to act on.
      showError(t('widgets.sidebars.sshKeyDetails.deleteError'), error)
    } finally {
      setIsProcessing(false)
    }
  }, [keyId, onClose, t])

  if (!key) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        {/* Keep a way out even if the fetch never resolves. */}
        <SidebarCloseButton onClose={onClose} />
        <div className="text-neutral-4">
          {t('widgets.sidebars.sshKeyDetails.loading')}
        </div>
      </div>
    )
  }

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
      {/* Back button */}
      <SidebarCloseButton onClose={onClose} />

      <h2 className="text-lg font-bold uppercase">
        {isCA
          ? t('widgets.sidebars.sshKeyDetails.titleCA')
          : t('widgets.sidebars.sshKeyDetails.titleSSH')}
      </h2>

      {/* Alias */}
      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.sshKeyDetails.alias')}
        </label>
        <div className="font-mono text-sm text-neutral-2">{key.alias}</div>
      </div>

      {/* Key Type */}
      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.sshKeyDetails.keyType')}
        </label>
        <div className="flex items-center gap-2 text-sm text-neutral-2">
          <span className="bg-purple-5/20 text-purple-4 px-2 py-1 rounded">
            {key.key_type === 'ca'
              ? t('widgets.sidebars.sshKeyDetails.keyTypeCA')
              : t('widgets.sidebars.sshKeyDetails.keyTypeSSH')}
          </span>
          {key.has_private_key && (
            <span className="bg-lime-6/20 text-lime-5 px-2 py-1 rounded">
              {t('widgets.sidebars.sshKeyDetails.connectionKey')}
            </span>
          )}
        </div>
      </div>

      {/* Fingerprint */}
      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.sshKeyDetails.fingerprint')}
        </label>
        <div className="font-mono text-sm text-neutral-2 break-all">
          {key.fingerprint}
        </div>
      </div>

      {/* Trusted on (CA keys only): where the CA is trusted, with per-unit
          removal — mirrors a user's unit memberships. Adding a placement is
          done by dragging the CA row onto a unit in the rail. */}
      {isCA && canReadTrusts && (
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('pages.credentials.keysTable.trustedOn')}
          </label>
          {placements.length === 0 ? (
            <p className="text-xs text-neutral-6">
              {t('pages.credentials.trustedCA.notTrusted')}
            </p>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {placements.map((p) => (
                <span
                  key={p.key}
                  className="flex items-center gap-1.5 rounded border border-neutral-11 px-2 py-0.5 text-xs text-neutral-3"
                >
                  {p.label}
                  {p.key !== 'node' ? (
                    <button
                      type="button"
                      disabled={isProcessing}
                      className="text-neutral-7 hover:text-status-error"
                      onClick={() => void removePlacement(p)}
                    >
                      ×
                    </button>
                  ) : null}
                </span>
              ))}
            </div>
          )}
          <p className="text-xs text-neutral-6">
            {t('pages.credentials.trustedCA.addByDragHint')}
          </p>
        </div>
      )}

      {/* Created/Updated dates */}
      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.sshKeyDetails.created')}
        </label>
        <div className="text-sm text-neutral-4 font-mono">
          {new Date(key.created_at).toLocaleString()}
        </div>
      </div>

      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.sshKeyDetails.updated')}
        </label>
        <div className="text-sm text-neutral-4 font-mono">
          {new Date(key.updated_at).toLocaleString()}
        </div>
      </div>

      {/* Actions */}
      <div className="flex flex-col gap-3 pt-4">
        <Button
          size="lg"
          onClick={() => void apiSSHKeys.downloadSSHKeyPublicKey(keyId)}
          disabled={isProcessing}
          kind="secondary"
          grow
        >
          {t('widgets.sidebars.sshKeyDetails.downloadPublicKey')}
        </Button>
        {canManageKeys && (
          <>
            <Button
              size="lg"
              onClick={handleEdit}
              disabled={isProcessing}
              kind="secondary"
              grow
            >
              {t('widgets.sidebars.sshKeyDetails.edit')}
            </Button>
            <Button
              size="lg"
              onClick={() => void openDeleteConfirm()}
              disabled={isProcessing}
              variant="destructive"
              grow
            >
              {t('widgets.sidebars.sshKeyDetails.delete')}
            </Button>
          </>
        )}
      </div>
      <ConfirmDialog
        open={isDeleteConfirmOpen}
        title={
          isCA
            ? t('widgets.sidebars.sshKeyDetails.deleteConfirmTitleCA')
            : t('widgets.sidebars.sshKeyDetails.deleteConfirmTitleSSH')
        }
        message={
          isCA
            ? t('widgets.sidebars.sshKeyDetails.deleteConfirmMessageCA')
            : [
                t('widgets.sidebars.sshKeyDetails.deleteConfirmMessageSSH'),
                deleteImpact && deleteImpact.users > 0
                  ? t('widgets.sidebars.sshKeyDetails.deleteImpactUsers', {
                      count: deleteImpact.users,
                    })
                  : '',
                deleteImpact && deleteImpact.locked > 0
                  ? t('widgets.sidebars.sshKeyDetails.deleteImpactLocked', {
                      count: deleteImpact.locked,
                    })
                  : '',
              ]
                .filter(Boolean)
                .join(' ')
        }
        confirmText={t('widgets.sidebars.sshKeyDetails.delete')}
        destructive
        onConfirm={handleDelete}
        onCancel={() => setIsDeleteConfirmOpen(false)}
      />
    </div>
  )
}
