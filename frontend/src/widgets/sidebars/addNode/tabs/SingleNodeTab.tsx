import { useNavigate } from '@tanstack/react-router'
import { FC, useEffect, useRef, useState, type SyntheticEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { SidebarEnum } from '@/widgets/sidebars/types'

import {
  deviceCapRefusal,
  isDeviceCapUnverified,
  type DeviceCapRefusal,
} from '@/entities/entitlement/deviceCap'
import { DeviceCapNotice } from '@/entities/entitlement/ui/DeviceCapNotice'
import { nodesModel } from '@/entities/node'
import { postCreateNode } from '@/entities/node/api/postCreateNode.ts'
import { useServerCapabilities } from '@/entities/serverCapabilities'
import { sshKeysModel, useSSHKeys } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarProps } from '../../types'
import { consoleLoginError, ConsoleLoginFields } from '../ConsoleLoginFields'

// SingleNodeTab pre-creates one node (per-node token, auto-approved). On success
// it opens the node's card, where the SD image (full boot bundle baked with this
// node's token) can be downloaded. On first boot the agent binds the hardware to
// this node and, if network-first is enabled, the platform flashes the EEPROM.
export const SingleNodeTab: FC<SidebarProps> = ({ onClose }) => {
  const { t } = useTranslation()
  const caps = useServerCapabilities()
  const navigate = useNavigate()
  const { keys } = useSSHKeys()
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  // The device cap is its own notice, not an error line: the fleet keeps
  // working and the remedy is a link, which a red sentence cannot carry.
  const [capRefusal, setCapRefusal] = useState<DeviceCapRefusal | null>(null)
  const nameInputRef = useRef<HTMLInputElement>(null)
  const [sshKeyId, setSSHKeyId] = useState<string>('')
  const [defaultUsername, setDefaultUsername] = useState<string>('')
  const [defaultPassword, setDefaultPassword] = useState<string>('')
  const [networkFirst, setNetworkFirst] = useState(true)

  useEffect(() => {
    sshKeysModel.fetchSSHKeysFx()
  }, [])

  useEffect(() => {
    nameInputRef.current?.focus()
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
    setCapRefusal(null)

    const name = defaultTrim(nameInputRef.current?.value)
    if (!name) {
      setFormError(t('widgets.sidebars.addNode.single.nameRequired'))
      return
    }
    const credentialError = consoleLoginError(defaultUsername, defaultPassword)
    if (credentialError) {
      setFormError(credentialError)
      return
    }

    setIsProcessing(true)
    try {
      const newNode = await postCreateNode({
        name,
        ssh_key_id: sshKeyId || undefined,
        default_username: defaultUsername.trim(),
        default_password: defaultPassword.trim(),
        // Only meaningful when the install can bake EEPROMs; otherwise the
        // flag would arm a one-shot that never fires.
        provision_network_first: caps.eeprom_signer && networkFirst,
      })
      await nodesModel.fetchNodesFx()
      // Land on the new node's card; the SD image / adoption file live there.
      navigate({
        to: '.',
        search: (old) => ({
          ...old,
          sidebar: SidebarEnum.Node,
          nodeId: newNode.id,
          view: undefined,
        }),
        replace: true,
      })
    } catch (error: unknown) {
      const cap = deviceCapRefusal(error)
      if (cap) {
        setCapRefusal(cap)
        return
      }
      if (isDeviceCapUnverified(error)) {
        setFormError(t('entities.entitlement.deviceCap.unverified'))
        return
      }
      setFormError(
        extractApiErrorMessage(
          error,
          t('widgets.sidebars.addNode.single.createFailed'),
        ),
      )
    } finally {
      setIsProcessing(false)
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.addNode.single.intro')}
      </div>

      {capRefusal && <DeviceCapNotice refusal={capRefusal} />}
      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <form className="flex flex-col gap-5" onSubmit={handleFormSubmit}>
        <Input
          ref={nameInputRef}
          id="name"
          name="name"
          type="text"
          label={t('widgets.sidebars.addNode.single.nodeNameLabel')}
          placeholder={t('widgets.sidebars.addNode.single.nodeNamePlaceholder')}
          required
          error={!!formError}
          autoComplete="off"
          disabled={isProcessing}
        />

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.addNode.single.sshKeyLabel')}{' '}
            <span className="normal-case text-neutral-4">
              {t('widgets.sidebars.addNode.single.sshKeyOptional')}
            </span>
          </label>
          {/*
            The key is genuinely optional: a node adopts over its own token, not
            over SSH, and node.Service.Create leaves SSHKeyID nil when none is
            given. This form used to disable both this select and the Create
            button on keys.length === 0 and explain it with "Please create an
            SSH key first" — so every brand-new account, which has no keys by
            construction, met a greyed-out primary button on the first thing it
            ever does, for a reason that was not true.
          */}
          <select
            id="ssh_key_id"
            name="ssh_key_id"
            value={sshKeyId}
            onChange={(e) => setSSHKeyId(e.target.value)}
            disabled={isProcessing}
            className="w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <option value="">
              {t('widgets.sidebars.addNode.single.noKeyOption')}
            </option>
            {keys.map((key) => (
              <option key={key.id} value={key.id}>
                {key.alias}{' '}
                {key.is_default
                  ? t('widgets.sidebars.addNode.single.defaultSuffix')
                  : ''}
              </option>
            ))}
          </select>
          {keys.length === 0 && (
            <div className="text-xs text-neutral-4">
              {t('widgets.sidebars.addNode.single.noSSHKeys')}
            </div>
          )}
        </div>

        <ConsoleLoginFields
          timing="after-os-install"
          username={defaultUsername}
          password={defaultPassword}
          onUsernameChange={setDefaultUsername}
          onPasswordChange={setDefaultPassword}
          disabled={isProcessing}
        />

        {/* Network-first needs the EEPROM signer; on an install that runs
            none (caps.eeprom_signer) the flag would silently never fire, so
            it is hidden. */}
        {caps.eeprom_signer && (
          <label className="flex items-start gap-2.5 text-sm text-neutral-2 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={networkFirst}
              onChange={(e) => setNetworkFirst(e.target.checked)}
              disabled={isProcessing}
              className="mt-0.5 w-4 h-4 accent-lime-6 cursor-pointer"
            />
            <span>
              {t('widgets.sidebars.addNode.single.networkFirstCheckbox')}
            </span>
          </label>
        )}

        <div className="flex gap-3">
          <Button
            size="lg"
            variant={formError ? 'destructive' : 'normal'}
            type="submit"
            disabled={isProcessing}
            grow
          >
            {isProcessing
              ? t('widgets.sidebars.addNode.single.creating')
              : t('widgets.sidebars.addNode.single.createNode')}
          </Button>
          <Button
            size="lg"
            kind="secondary"
            type="button"
            onClick={onClose}
            disabled={isProcessing}
          >
            {t('widgets.sidebars.addNode.single.cancel')}
          </Button>
        </div>
      </form>
    </div>
  )
}

const defaultTrim = (v: string | undefined): string => (v ?? '').trim()
