import { FC, useEffect, useState, type SyntheticEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { apiMasterTokens, masterTokensModel } from '@/entities/masterToken'
import { ousModel, useOUs } from '@/entities/ou'
import { sshKeysModel, useSSHKeys } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { useCopyText } from '@/shared/utils/copyText'

import { SidebarCloseButton } from '../closeButton'
import { SidebarProps } from '../types'

export const SidebarCreateMasterToken: FC<SidebarProps> = ({ onClose }) => {
  const { t } = useTranslation()
  // A CA key signs certificates; it is not a key a device can be given.
  const { keys: allKeys } = useSSHKeys()
  const keys = allKeys.filter((key) => key.key_type !== 'ca')
  const { ous } = useOUs()
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [sshKeyId, setSSHKeyId] = useState<string>('')
  const [ouId, setOUId] = useState<string>('')
  const [defaultUsername, setDefaultUsername] = useState<string>('')
  const [defaultPassword, setDefaultPassword] = useState<string>('')
  const [createdToken, setCreatedToken] = useState<string | null>(null)
  const [adoptionFile, setAdoptionFile] = useState<string | null>(null)
  const { state: copyState, copy } = useCopyText()

  useEffect(() => {
    // Reload SSH keys when opening the sidebar; the store may be empty if
    // the user never visited a page that fetches them.
    sshKeysModel.fetchSSHKeysFx()
    ousModel.fetchOUsFx()
  }, [])

  useEffect(() => {
    const defaultKey = keys.find((k) => k.is_default)
    if (defaultKey) {
      setSSHKeyId(defaultKey.id)
    } else if (keys.length > 0) {
      setSSHKeyId(keys[0].id)
    }
  }, [keys])

  const handleFormSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setFormError(null)

    if (!name.trim()) {
      setFormError(t('widgets.sidebars.createMasterToken.nameRequired'))
      return
    }

    if (!defaultUsername.trim()) {
      setFormError(t('widgets.sidebars.createMasterToken.usernameRequired'))
      return
    }

    if (!defaultPassword.trim()) {
      setFormError(t('widgets.sidebars.createMasterToken.passwordRequired'))
      return
    }

    setIsProcessing(true)

    try {
      const result = await apiMasterTokens.postCreateMasterToken({
        name: name.trim(),
        default_username: defaultUsername.trim(),
        default_password: defaultPassword.trim(),
        ssh_key_id: sshKeyId || undefined,
        ou_id: ouId || undefined,
      })

      // Store the token and adoption file (only available once)
      if (result.token) {
        setCreatedToken(result.token)
      }
      if (result.adoption_file) {
        setAdoptionFile(result.adoption_file)
      }

      await masterTokensModel.fetchMasterTokensFx()
    } catch (error: unknown) {
      setFormError(
        extractApiErrorMessage(
          error,
          t('widgets.sidebars.createMasterToken.createError'),
        ),
      )
    } finally {
      setIsProcessing(false)
    }
  }

  const handleCopyToken = () => {
    if (createdToken) {
      void copy(createdToken)
    }
  }

  const handleDownloadAdoptionFile = () => {
    if (!adoptionFile) return

    // The adoption file from backend is already base64 encoded
    const blob = new Blob([adoptionFile], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'node-adoption.txt'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  }

  // Show success state with token
  if (createdToken) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        <SidebarCloseButton onClose={onClose} />

        <h2 className="text-lg font-bold uppercase">
          {t('widgets.sidebars.createMasterToken.createdTitle')}
        </h2>

        <div className="bg-lime-6/10 border border-lime-6/50 text-lime-5 px-4 py-3 rounded text-sm">
          {t('widgets.sidebars.createMasterToken.createdNotice')}
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.createMasterToken.masterTokenLabel')}
          </label>
          <div className="flex gap-2">
            <input
              type="text"
              value={createdToken}
              readOnly
              className="flex-1 px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 font-mono"
            />
            <Button onClick={handleCopyToken} size="md">
              {copyState === 'copied'
                ? t('widgets.sidebars.createMasterToken.copied')
                : copyState === 'failed'
                  ? t('shared.copy.manual')
                  : t('widgets.sidebars.createMasterToken.copy')}
            </Button>
          </div>
        </div>

        <div className="flex flex-col gap-3 pt-4">
          {adoptionFile && (
            <Button size="lg" onClick={handleDownloadAdoptionFile} grow>
              {t('widgets.sidebars.createMasterToken.downloadAdoptionFile')}
            </Button>
          )}
          <Button size="lg" onClick={onClose} kind="secondary" grow>
            {t('widgets.sidebars.createMasterToken.done')}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
      <SidebarCloseButton onClose={onClose} />

      <h2 className="text-lg font-bold uppercase">
        {t('widgets.sidebars.createMasterToken.title')}
      </h2>

      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.createMasterToken.intro')}
      </div>

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <form onSubmit={handleFormSubmit} className="flex flex-col gap-6">
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.createMasterToken.tokenNameLabel')}
          </label>
          <Input
            type="text"
            name="name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t(
              'widgets.sidebars.createMasterToken.tokenNamePlaceholder',
            )}
            required
            disabled={isProcessing}
          />
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.createMasterToken.tokenNameHint')}
          </div>
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.createMasterToken.sshKeyLabel')}
          </label>
          <select
            name="ssh_key_id"
            value={sshKeyId}
            onChange={(e) => setSSHKeyId(e.target.value)}
            disabled={isProcessing || keys.length === 0}
            className="w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {keys.length === 0 ? (
              <option value="">
                {t('widgets.sidebars.createMasterToken.noSSHKeys')}
              </option>
            ) : (
              keys.map((key) => (
                <option key={key.id} value={key.id}>
                  {key.alias}{' '}
                  {key.is_default
                    ? t('widgets.sidebars.createMasterToken.defaultSuffix')
                    : ''}
                </option>
              ))
            )}
          </select>
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.createMasterToken.sshKeyHint')}
          </div>
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.createMasterToken.adoptionOULabel')}
          </label>
          <select
            name="ou_id"
            value={ouId}
            onChange={(e) => setOUId(e.target.value)}
            disabled={isProcessing}
            className="w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <option value="">
              {t('widgets.sidebars.createMasterToken.adoptionOUNone')}
            </option>
            {ous.map((ou) => (
              <option key={ou.id} value={ou.id}>
                {ou.name}
              </option>
            ))}
          </select>
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.createMasterToken.adoptionOUHint')}
          </div>
        </div>

        <Input
          type="text"
          name="default_username"
          label={t('widgets.sidebars.createMasterToken.defaultUsernameLabel')}
          placeholder={t(
            'widgets.sidebars.createMasterToken.defaultUsernamePlaceholder',
          )}
          value={defaultUsername}
          onChange={(e) => setDefaultUsername(e.target.value)}
          required
          disabled={isProcessing}
        />

        <Input
          type="password"
          name="default_password"
          label={t('widgets.sidebars.createMasterToken.defaultPasswordLabel')}
          placeholder={t(
            'widgets.sidebars.createMasterToken.defaultPasswordPlaceholder',
          )}
          value={defaultPassword}
          onChange={(e) => setDefaultPassword(e.target.value)}
          required
          disabled={isProcessing}
        />

        <Button type="submit" size="lg" disabled={isProcessing} grow>
          {isProcessing
            ? t('widgets.sidebars.createMasterToken.creating')
            : t('widgets.sidebars.createMasterToken.submit')}
        </Button>
      </form>
    </div>
  )
}
