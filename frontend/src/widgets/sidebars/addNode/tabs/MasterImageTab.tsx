import { FC, useEffect, useState, type SyntheticEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { apiProvisioning } from '@/entities/provisioning'
import { sshKeysModel, useSSHKeys } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { InfoHint } from '@/shared/ui/infoHint'
import { Input } from '@/shared/ui/input'
import { SourceOfferNote } from '@/shared/ui/sourceOfferNote'
import {
  earliestExpiryDay,
  expiryEndOfDay,
  isSelectableExpiryDay,
} from '@/shared/utils/expiryDate'

import { SidebarProps } from '../../types'
import { consoleLoginError, ConsoleLoginFields } from '../ConsoleLoginFields'

type ApiErrorLike = {
  response?: { data?: unknown }
  message?: string
}

// Best-effort extraction of an error message. Because the endpoint responds
// with a blob, an error body may be a Blob rather than parsed JSON.
const extractErrorMessage = async (
  error: ApiErrorLike,
): Promise<string | null> => {
  const readMessage = (data: unknown): string | null => {
    if (data && typeof data === 'object') {
      const record = data as Record<string, unknown>
      if (typeof record.message === 'string') return record.message
      if (typeof record.error === 'string') return record.error
    }
    return null
  }
  const data = error.response?.data
  if (data instanceof Blob) {
    try {
      return readMessage(JSON.parse(await data.text()))
    } catch {
      return null
    }
  }
  return readMessage(data) ?? error.message ?? null
}

// MasterImageTab mints a master token and downloads a bulk SD boot bundle. Nodes
// booted from it land pending approval; the SD card must stay in until approved.
export const MasterImageTab: FC<SidebarProps> = () => {
  const { t } = useTranslation()
  const { keys } = useSSHKeys()
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [successMessage, setSuccessMessage] = useState<string | null>(null)
  const [name, setName] = useState('sd-provisioning')
  const [sshKeyId, setSSHKeyId] = useState<string>('')
  const [defaultUsername, setDefaultUsername] = useState<string>('')
  const [defaultPassword, setDefaultPassword] = useState<string>('')
  const [expiresAt, setExpiresAt] = useState<string>('')

  useEffect(() => {
    sshKeysModel.fetchSSHKeysFx()
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
    setSuccessMessage(null)

    if (!name.trim()) {
      setFormError(t('widgets.sidebars.createSDProvisioning.nameRequired'))
      return
    }
    const credentialError = consoleLoginError(defaultUsername, defaultPassword)
    if (credentialError) {
      setFormError(credentialError)
      return
    }
    if (expiresAt && !isSelectableExpiryDay(expiresAt)) {
      setFormError(t('widgets.sidebars.createSDProvisioning.expiresAtPast'))
      return
    }

    setIsProcessing(true)
    try {
      await apiProvisioning.downloadSDProvisioningImage({
        name: name.trim(),
        default_username: defaultUsername.trim(),
        default_password: defaultPassword.trim(),
        ssh_key_id: sshKeyId || undefined,
        expires_at: (expiresAt && expiryEndOfDay(expiresAt)) || undefined,
      })
      setSuccessMessage(t('widgets.sidebars.createSDProvisioning.success'))
    } catch (error) {
      const message = await extractErrorMessage(error as ApiErrorLike)
      setFormError(message || t('widgets.sidebars.createSDProvisioning.error'))
    } finally {
      setIsProcessing(false)
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.createSDProvisioning.description')}
        <span className="whitespace-nowrap">
          &nbsp;
          <InfoHint
            text={t('widgets.sidebars.createSDProvisioning.descriptionTooltip')}
          />
        </span>
      </div>

      <SourceOfferNote subject="bundle" />

      <div className="bg-amber-6/10 border border-amber-6/50 text-amber-4 px-4 py-2 rounded text-xs">
        Keep the SD card in the device until the node is approved and appears in
        the node list. The adoption token is on the card; removing it before the
        OS is installed leaves the device without an identity.
      </div>

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      {successMessage && (
        <div className="bg-lime-6/10 border border-lime-6/50 text-lime-5 px-4 py-3 rounded text-sm">
          {successMessage}
        </div>
      )}

      <form onSubmit={handleFormSubmit} className="flex flex-col gap-6">
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.createSDProvisioning.nameLabel')}
          </label>
          <Input
            type="text"
            name="name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t(
              'widgets.sidebars.createSDProvisioning.namePlaceholder',
            )}
            required
            disabled={isProcessing}
          />
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.createSDProvisioning.nameHint')}
          </div>
        </div>

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.createSDProvisioning.sshKeyLabel')}
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
                {t('widgets.sidebars.createSDProvisioning.sshKeyEmpty')}
              </option>
            ) : (
              keys.map((key) => (
                <option key={key.id} value={key.id}>
                  {key.alias}{' '}
                  {key.is_default
                    ? t(
                        'widgets.sidebars.createSDProvisioning.sshKeyDefaultSuffix',
                      )
                    : ''}
                </option>
              ))
            )}
          </select>
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.createSDProvisioning.sshKeyHint')}
          </div>
        </div>

        <ConsoleLoginFields
          timing="after-os-install"
          username={defaultUsername}
          password={defaultPassword}
          onUsernameChange={setDefaultUsername}
          onPasswordChange={setDefaultPassword}
          disabled={isProcessing}
        />

        <div className="flex flex-col gap-2">
          <Input
            type="date"
            name="expires_at"
            label={t('widgets.sidebars.createSDProvisioning.expiresAtLabel')}
            value={expiresAt}
            min={earliestExpiryDay()}
            onChange={(e) => setExpiresAt(e.target.value)}
            disabled={isProcessing}
          />
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.createSDProvisioning.expiresAtHint')}
          </div>
        </div>

        <Button type="submit" size="lg" disabled={isProcessing} grow>
          {isProcessing
            ? t('widgets.sidebars.createSDProvisioning.downloading')
            : t('widgets.sidebars.createSDProvisioning.download')}
        </Button>
      </form>
    </div>
  )
}
