import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { TagsInput } from '@/widgets/tagsInput'

import {
  apiMasterTokens,
  masterTokensModel,
  useMasterToken,
  useMasterTokens,
} from '@/entities/masterToken'
import { ousModel, useOUs } from '@/entities/ou'
import { Actions, useCan } from '@/entities/permissions/hooks'
import { sshKeysModel, useSSHKeys } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { Input } from '@/shared/ui/input'
import { showError } from '@/shared/ui/toast'

import { SidebarCloseButton } from '../closeButton'
import { SidebarProps } from '../types'

export const SidebarMasterTokenDetails: FC<
  SidebarProps & { tokenId: string }
> = ({ tokenId, onClose }) => {
  const { t } = useTranslation()
  const { token } = useMasterToken({ tokenId })
  const canManageTokens = useCan()(Actions.MasterTokenManage)
  // A CA key signs certificates; it is not a key a device can be given.
  const { keys: allKeys } = useSSHKeys()
  const keys = allKeys.filter((key) => key.key_type !== 'ca')
  const [isProcessing, setIsProcessing] = useState(false)
  const [isDeleteConfirmOpen, setIsDeleteConfirmOpen] = useState(false)
  const [isEditing, setIsEditing] = useState(false)
  const [editUsername, setEditUsername] = useState('')
  const [editPassword, setEditPassword] = useState('')
  const [editSSHKeyId, setEditSSHKeyId] = useState('')
  const [editOUId, setEditOUId] = useState('')
  const [editTags, setEditTags] = useState<string[]>([])
  const { ous } = useOUs()
  const { tokens: allTokens } = useMasterTokens()
  const tokenTagSuggestions = [...new Set(allTokens.flatMap((t) => t.tags))]

  useEffect(() => {
    // Fetch tokens if not in store, for a viewer who may read them.
    if (!token && canManageTokens) {
      masterTokensModel.fetchMasterTokensFx().catch(() => {})
    }
  }, [tokenId, token, canManageTokens])

  useEffect(() => {
    // Reload SSH keys and OUs when opening the sidebar; the stores may be
    // empty if the user never visited a page that fetches them.
    sshKeysModel.fetchSSHKeysFx()
    ousModel.fetchOUsFx()
  }, [])

  // Every edit field starts from the token as stored, on each Edit open and
  // on Cancel, so an abandoned change is never saved by a later edit.
  const resetEdits = useCallback(() => {
    setEditPassword('')
    if (token) {
      setEditUsername(token.default_username || '')
      setEditSSHKeyId(token.ssh_key_id || '')
      setEditOUId(token.ou_id || '')
      setEditTags(token.tags ?? [])
    }
  }, [token])

  useEffect(() => {
    resetEdits()
  }, [resetEdits])

  const handleToggleActive = useCallback(async () => {
    if (!token) return

    try {
      setIsProcessing(true)
      await apiMasterTokens.putUpdateMasterToken(tokenId, {
        is_active: !token.is_active,
      })
      await masterTokensModel.fetchMasterTokensFx()
    } catch (error) {
      // console.error is invisible to the operator: the switch snapped back
      // and nothing said why. The server's reason goes to the toast.
      showError(
        t('widgets.sidebars.masterTokenDetails.toggleStatusError'),
        error,
      )
    } finally {
      setIsProcessing(false)
    }
  }, [token, tokenId])

  const handleSaveCredentials = useCallback(async () => {
    try {
      setIsProcessing(true)
      await apiMasterTokens.putUpdateMasterToken(tokenId, {
        default_username: editUsername,
        default_password: editPassword || undefined,
        // "" clears the key: adopted devices then get none from this token.
        ssh_key_id: editSSHKeyId,
        // "" clears the adoption target OU.
        ou_id: editOUId,
        tags: editTags,
      })
      await masterTokensModel.fetchMasterTokensFx()
      setIsEditing(false)
      setEditPassword('')
    } catch (error) {
      // Note this one stays in edit mode on failure: setIsEditing(false) is
      // above, inside the try, so a rejected save keeps the user's input
      // rather than discarding it behind a closed form.
      showError(
        t('widgets.sidebars.masterTokenDetails.saveCredentialsError'),
        error,
      )
    } finally {
      setIsProcessing(false)
    }
  }, [tokenId, editUsername, editPassword, editSSHKeyId, editOUId, editTags])

  const handleDelete = useCallback(async () => {
    setIsDeleteConfirmOpen(false)
    onClose()
    try {
      setIsProcessing(true)
      await apiMasterTokens.deleteMasterToken(tokenId)
      await masterTokensModel.fetchMasterTokensFx()
    } catch (error) {
      // The panel is already closed by the time this runs (onClose() is the
      // first statement), so a toast is the ONLY way the failure can reach
      // the operator — otherwise a token that is still live looks deleted.
      showError(t('widgets.sidebars.masterTokenDetails.deleteError'), error)
    } finally {
      setIsProcessing(false)
    }
  }, [tokenId, onClose])

  if (!token) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        <div className="text-neutral-4">
          {t('widgets.sidebars.masterTokenDetails.loading')}
        </div>
      </div>
    )
  }

  const formatDate = (timestamp: number | string) => {
    const d =
      typeof timestamp === 'number'
        ? new Date(timestamp * 1000)
        : new Date(timestamp)
    return d.toLocaleString()
  }

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
      <SidebarCloseButton onClose={onClose} />

      <h2 className="text-lg font-bold uppercase">
        {t('widgets.sidebars.masterTokenDetails.title')}
      </h2>

      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.masterTokenDetails.nameLabel')}
        </label>
        <div className="text-sm text-neutral-2">{token.name}</div>
      </div>

      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.masterTokenDetails.statusLabel')}
        </label>
        <div className="text-sm">
          {token.is_active ? (
            <span className="bg-lime-6 text-neutral-14 px-2 py-1 rounded">
              {t('widgets.sidebars.masterTokenDetails.active')}
            </span>
          ) : (
            <span className="bg-neutral-8 text-neutral-2 px-2 py-1 rounded">
              {t('widgets.sidebars.masterTokenDetails.inactive')}
            </span>
          )}
        </div>
      </div>

      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.masterTokenDetails.createdLabel')}
        </label>
        <div className="text-sm text-neutral-4 font-mono">
          {formatDate(token.created_at)}
        </div>
      </div>

      {token.last_used_at && (
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.masterTokenDetails.lastUsedLabel')}
          </label>
          <div className="text-sm text-neutral-4 font-mono">
            {formatDate(token.last_used_at)}
          </div>
        </div>
      )}

      {token.expires_at && (
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.masterTokenDetails.expiresLabel')}
          </label>
          <div className="text-sm text-neutral-4 font-mono">
            {formatDate(token.expires_at)}
          </div>
        </div>
      )}

      <div className="border-t border-neutral-11 pt-4 flex flex-col gap-4">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-bold text-neutral-5 uppercase">
            {t('widgets.sidebars.masterTokenDetails.defaultCredentials')}
          </h3>
          {!isEditing && (
            <button
              type="button"
              onClick={() => {
                resetEdits()
                setIsEditing(true)
              }}
              className="text-xs text-neutral-5 hover:text-neutral-2 uppercase"
            >
              {t('widgets.sidebars.masterTokenDetails.edit')}
            </button>
          )}
        </div>

        {isEditing ? (
          <div className="flex flex-col gap-4">
            <div className="flex flex-col gap-2">
              <label className="text-sm font-medium text-neutral-7 uppercase">
                {t('widgets.sidebars.masterTokenDetails.sshKeyLabel')}
              </label>
              <select
                value={editSSHKeyId}
                onChange={(e) => setEditSSHKeyId(e.target.value)}
                disabled={isProcessing || keys.length === 0}
                className="w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <option value="">
                  {t('widgets.sidebars.masterTokenDetails.none')}
                </option>
                {keys.map((key) => (
                  <option key={key.id} value={key.id}>
                    {key.alias}{' '}
                    {key.is_default
                      ? t('widgets.sidebars.masterTokenDetails.defaultSuffix')
                      : ''}
                  </option>
                ))}
              </select>
            </div>

            <div className="flex flex-col gap-2">
              <label className="text-sm font-medium text-neutral-7 uppercase">
                {t('widgets.sidebars.masterTokenDetails.adoptionOULabel')}
              </label>
              <select
                value={editOUId}
                onChange={(e) => setEditOUId(e.target.value)}
                disabled={isProcessing}
                className="w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              >
                <option value="">
                  {t('widgets.sidebars.masterTokenDetails.adoptionOUNone')}
                </option>
                {ous.map((ou) => (
                  <option key={ou.id} value={ou.id}>
                    {ou.name}
                  </option>
                ))}
              </select>
              <div className="text-xs text-neutral-6">
                {t('widgets.sidebars.masterTokenDetails.adoptionOUHint')}
              </div>
            </div>

            <TagsInput
              value={editTags}
              onChange={setEditTags}
              suggestions={tokenTagSuggestions}
              disabled={isProcessing}
            />

            <Input
              type="text"
              label={t(
                'widgets.sidebars.masterTokenDetails.defaultUsernameInputLabel',
              )}
              value={editUsername}
              onChange={(e) => setEditUsername(e.target.value)}
              disabled={isProcessing}
            />

            <Input
              type="password"
              label={t('widgets.sidebars.masterTokenDetails.newPasswordLabel')}
              placeholder={t(
                'widgets.sidebars.masterTokenDetails.newPasswordPlaceholder',
              )}
              value={editPassword}
              onChange={(e) => setEditPassword(e.target.value)}
              disabled={isProcessing}
            />

            <div className="flex gap-2">
              <Button
                size="md"
                onClick={handleSaveCredentials}
                disabled={isProcessing}
                grow
              >
                {isProcessing
                  ? t('widgets.sidebars.masterTokenDetails.saving')
                  : t('widgets.sidebars.masterTokenDetails.save')}
              </Button>
              <Button
                size="md"
                kind="secondary"
                onClick={() => {
                  setIsEditing(false)
                  resetEdits()
                }}
                disabled={isProcessing}
              >
                {t('widgets.sidebars.masterTokenDetails.cancel')}
              </Button>
            </div>
          </div>
        ) : (
          <>
            <div className="flex flex-col gap-2">
              <label className="text-sm font-medium text-neutral-7 uppercase">
                {t('widgets.sidebars.masterTokenDetails.defaultUsernameLabel')}
              </label>
              <div className="text-sm text-neutral-2 font-mono">
                {token.default_username || '—'}
              </div>
            </div>

            <div className="flex flex-col gap-2">
              <label className="text-sm font-medium text-neutral-7 uppercase">
                {t('widgets.sidebars.masterTokenDetails.sshKeyLabel')}
              </label>
              <div className="text-sm text-neutral-2">
                {token.ssh_key_id
                  ? keys.find((k) => k.id === token.ssh_key_id)?.alias ||
                    token.ssh_key_id
                  : '—'}
              </div>
            </div>
          </>
        )}
      </div>

      <div className="text-sm text-neutral-5 bg-neutral-13 px-4 py-3 rounded">
        {t('widgets.sidebars.masterTokenDetails.adoptionFileNote')}
      </div>

      <div className="flex flex-col gap-3 pt-4">
        <Button
          size="lg"
          onClick={handleToggleActive}
          disabled={isProcessing}
          kind="secondary"
          grow
        >
          {token.is_active
            ? t('widgets.sidebars.masterTokenDetails.deactivate')
            : t('widgets.sidebars.masterTokenDetails.activate')}
        </Button>
        <Button
          size="lg"
          onClick={() => setIsDeleteConfirmOpen(true)}
          disabled={isProcessing}
          variant="destructive"
          grow
        >
          {t('widgets.sidebars.masterTokenDetails.delete')}
        </Button>
      </div>
      <ConfirmDialog
        open={isDeleteConfirmOpen}
        title={t('widgets.sidebars.masterTokenDetails.deleteConfirmTitle')}
        message={t('widgets.sidebars.masterTokenDetails.deleteConfirmMessage')}
        confirmText={t('widgets.sidebars.masterTokenDetails.delete')}
        destructive
        onConfirm={handleDelete}
        onCancel={() => setIsDeleteConfirmOpen(false)}
      />
    </div>
  )
}
