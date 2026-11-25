import { useNavigate } from '@tanstack/react-router'
import {
  FC,
  useCallback,
  useEffect,
  useState,
  type SyntheticEvent,
} from 'react'
import { useTranslation } from 'react-i18next'

import { SidebarEnum } from '@/widgets/sidebars/types'

import { apiSSHKeys, sshKeysModel, useSSHKey } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarProps } from '../types'

export const SidebarEditSSHKey: FC<SidebarProps & { keyId: string }> = ({
  keyId,
  onClose: _onClose,
}) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { key } = useSSHKey({ keyId })
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [alias, setAlias] = useState('')
  const [keyContent, setKeyContent] = useState('')
  const [isDefault, setIsDefault] = useState(false)

  useEffect(() => {
    // Fetch the key if not in store
    if (!key) {
      sshKeysModel.fetchSSHKeyFx(keyId)
    } else {
      setAlias(key.alias)
      setIsDefault(key.is_default)
      // Don't set keyContent - we never show it for security
    }
  }, [keyId, key])

  const handleCancel = useCallback(() => {
    navigate({
      to: '.',
      // Merge, don't replace — see sshKeyDetails.handleEdit (tab loss).
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        sidebar: SidebarEnum.SSHKeyDetails,
        keyId,
      }),
    })
  }, [navigate, keyId])

  const handleFormSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setFormError(null)

    if (!alias.trim()) {
      setFormError(t('widgets.sidebars.editSSHKey.aliasRequired'))
      return
    }

    setIsProcessing(true)

    try {
      const updateData: {
        alias?: string
        key_content?: string
        is_default?: boolean
      } = {}

      if (alias.trim() !== key?.alias) {
        updateData.alias = alias.trim()
      }

      if (keyContent.trim()) {
        updateData.key_content = keyContent.trim()
      }

      if (isDefault !== key?.is_default) {
        updateData.is_default = isDefault
      }

      if (Object.keys(updateData).length === 0) {
        setFormError(t('widgets.sidebars.editSSHKey.noChanges'))
        setIsProcessing(false)
        return
      }

      await apiSSHKeys.putUpdateSSHKey(keyId, updateData)

      await sshKeysModel.fetchSSHKeysFx()
      await sshKeysModel.fetchSSHKeyFx(keyId)
      // Navigate back to details view instead of closing
      handleCancel()
    } catch (error) {
      setFormError(
        extractApiErrorMessage(
          error,
          t('widgets.sidebars.editSSHKey.updateError'),
        ),
      )
    } finally {
      setIsProcessing(false)
    }
  }

  if (!key) {
    return (
      <div className="px-5 pt-0.5 pb-5 flex flex-col gap-6">
        {/* Keep a way out even if the fetch never resolves. */}
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={handleCancel}
            className="text-neutral-7 hover:text-neutral-5 text-sm"
          >
            {t('widgets.sidebars.editSSHKey.back')}
          </button>
        </div>
        <div className="text-neutral-4">
          {t('widgets.sidebars.editSSHKey.loading')}
        </div>
      </div>
    )
  }

  const isCA = key.key_type === 'ca'

  return (
    <div className="px-5 pt-0.5 pb-5 flex flex-col gap-6">
      {/* Back button */}
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={handleCancel}
          className="text-neutral-7 hover:text-neutral-5 text-sm"
        >
          {t('widgets.sidebars.editSSHKey.back')}
        </button>
      </div>

      <h2 className="text-lg font-bold uppercase">
        {isCA
          ? t('widgets.sidebars.editSSHKey.titleCA')
          : t('widgets.sidebars.editSSHKey.titleSSH')}
      </h2>

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <form onSubmit={handleFormSubmit} className="flex flex-col gap-6">
        {/* Alias */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.editSSHKey.aliasLabel')}
          </label>
          <Input
            type="text"
            name="alias"
            value={alias}
            onChange={(e) => setAlias(e.target.value)}
            placeholder={t('widgets.sidebars.editSSHKey.aliasPlaceholder')}
            required
            disabled={isProcessing}
          />
        </div>

        {/* Key Type (read-only) */}
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.editSSHKey.keyTypeLabel')}
          </label>
          <div className="text-sm text-neutral-2">
            <span className="bg-purple-5/20 text-purple-4 px-2 py-1 rounded">
              {key.key_type === 'ca'
                ? t('widgets.sidebars.editSSHKey.keyTypeCA')
                : t('widgets.sidebars.editSSHKey.keyTypeSSH')}
            </span>
          </div>
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.editSSHKey.keyTypeHint')}
          </div>
        </div>

        {/* Key Content (optional update). Not for a generated connection
            keypair: its stored private key is what adoption signs in with,
            and a new public half would no longer match it. */}
        {key.has_private_key ? (
          <div className="flex flex-col gap-1 text-xs text-neutral-4">
            <div>
              {t('widgets.sidebars.editSSHKey.fingerprint', {
                fingerprint: key.fingerprint,
              })}
            </div>
            <div>{t('widgets.sidebars.editSSHKey.generatedKeyFixed')}</div>
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium text-neutral-7 uppercase">
              {key.key_type === 'ca'
                ? t('widgets.sidebars.editSSHKey.updateKeyLabelCA')
                : t('widgets.sidebars.editSSHKey.updateKeyLabel')}
            </label>
            <textarea
              name="key_content"
              value={keyContent}
              onChange={(e) => setKeyContent(e.target.value)}
              placeholder={
                key.key_type === 'ca'
                  ? t('widgets.sidebars.editSSHKey.updateKeyPlaceholderCA')
                  : t('widgets.sidebars.editSSHKey.updateKeyPlaceholder')
              }
              className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5 font-mono min-h-[120px] resize-y"
              disabled={isProcessing}
            />
            <div className="text-xs text-neutral-4">
              {t('widgets.sidebars.editSSHKey.fingerprint', {
                fingerprint: key.fingerprint,
              })}
            </div>
          </div>
        )}

        {/* "Default key" is an SSH-key inventory concept; a CA is a pure trust
            anchor, so hide it for CA keys. Tags were dropped entirely (unused
            for filtering anywhere). */}
        {key.key_type !== 'ca' && (
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="is_default"
              checked={isDefault}
              onChange={(e) => setIsDefault(e.target.checked)}
              disabled={isProcessing}
              className="w-4 h-4"
            />
            <label
              htmlFor="is_default"
              className="text-sm font-medium text-neutral-7"
            >
              {t('widgets.sidebars.editSSHKey.setAsDefault')}
            </label>
          </div>
        )}

        {/* Submit and Cancel */}
        <div className="flex flex-col gap-3">
          <Button type="submit" size="lg" disabled={isProcessing} grow>
            {isProcessing
              ? t('widgets.sidebars.editSSHKey.updating')
              : t('widgets.sidebars.editSSHKey.submit')}
          </Button>
          <Button
            type="button"
            size="lg"
            onClick={handleCancel}
            disabled={isProcessing}
            kind="secondary"
            grow
          >
            {t('widgets.sidebars.editSSHKey.cancel')}
          </Button>
        </div>
      </form>
    </div>
  )
}
