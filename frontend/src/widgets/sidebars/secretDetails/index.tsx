import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SubjectAttachmentTargets } from '@/widgets/secretsAttachment/subjectTargets'

import { Actions, useCan } from '@/entities/permissions/hooks'
import {
  secretsModel,
  useSecret,
  type SecretKind,
  type UpdateSecretRequest,
} from '@/entities/secret'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { hasLineBreak, isEnvKey, isSecretName } from '@/shared/utils/formRules'

import { SidebarCloseButton } from '../closeButton'
import { SecretTagsInput } from '../secretTagsInput'
import { SidebarProps } from '../types'

// The `key` field label depends on the secret kind.
const keyLabelKey = {
  env: 'widgets.sidebars.secret.keyEnv',
  credential: 'widgets.sidebars.secret.keyCredential',
  opaque: 'widgets.sidebars.secret.keyOpaque',
} as const satisfies Record<SecretKind, string>

// Translation key for a secret kind badge/label.
const kindLabelKey = {
  env: 'entities.secret.kind.env',
  credential: 'entities.secret.kind.credential',
  opaque: 'entities.secret.kind.opaque',
} as const satisfies Record<SecretKind, string>

// Saving and deleting a secret ride on the account-wide secret grant; a viewer
// gets the read-only view the backend would enforce anyway.
export const SidebarSecretDetails: FC<SidebarProps & { secretId: string }> = ({
  secretId,
  onClose,
}) => {
  const { t } = useTranslation()
  const { secret } = useSecret({ secretId })
  const canManage = useCan()(Actions.SecretManage)
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [isDeleteConfirmOpen, setIsDeleteConfirmOpen] = useState(false)
  const [name, setName] = useState('')
  const [key, setKey] = useState('')
  const [tags, setTags] = useState<string[]>([])
  // Promote-only sensitivity: a variable can become a secret, never back.
  const [makeSensitive, setMakeSensitive] = useState(false)
  // Sensitive entries: empty field means "unchanged". Typing sets it; the
  // explicit "clear" action queues an empty-string update.
  // Variables: the field is prefilled with the current value and sent as-is
  // when it differs.
  const [newValue, setNewValue] = useState('')
  const [valueCleared, setValueCleared] = useState(false)

  useEffect(() => {
    // The list page may hold a different page of secrets; fetch the one we
    // need for the detail view.
    if (!secret) {
      secretsModel.fetchSecretFx(secretId)
    }
  }, [secretId, secret])

  useEffect(() => {
    if (secret) {
      setName(secret.name)
      setKey(secret.key)
      setTags(secret.tags ?? [])
      setMakeSensitive(false)
      setNewValue(secret.sensitive ? '' : (secret.value ?? ''))
      setValueCleared(false)
    }
  }, [secret])

  const handleSave = useCallback(async () => {
    if (!secret) return
    setFormError(null)
    if (!name.trim()) {
      setFormError(t('widgets.sidebars.secretDetails.nameRequired'))
      return
    }
    // Only a changed name is held to the reference rule, so an entry named
    // before the rule existed can still have its value or tags edited.
    if (name.trim() !== secret.name && !isSecretName(name)) {
      setFormError(t('widgets.sidebars.secret.nameInvalid'))
      return
    }
    // Likewise only a changed ENV name: a stored one that predates the rule
    // must not block rotating the value.
    if (
      secret.kind === 'env' &&
      key.trim() !== secret.key &&
      !isEnvKey(key.trim())
    ) {
      setFormError(t('widgets.sidebars.secret.envKeyInvalid'))
      return
    }
    if (secret.kind === 'env' && hasLineBreak(newValue)) {
      setFormError(t('widgets.sidebars.secret.envValueLineBreak'))
      return
    }
    try {
      setIsProcessing(true)
      const data: UpdateSecretRequest = {
        name: name.trim(),
        key: key.trim(),
      }
      if (secret.sensitive) {
        // Value is only sent when typed or explicitly cleared.
        if (newValue) {
          data.value = newValue
        } else if (valueCleared) {
          data.value = ''
        }
      } else {
        if (newValue !== (secret.value ?? '')) {
          data.value = newValue
        }
        if (makeSensitive) {
          data.sensitive = true
        }
      }
      if (JSON.stringify(secret.tags ?? []) !== JSON.stringify(tags)) {
        data.tags = tags
      }
      await secretsModel.updateSecretFx({ secretId, data })
      setValueCleared(false)
      setMakeSensitive(false)
    } catch (error) {
      setFormError(
        extractApiErrorMessage(
          error,
          t('widgets.sidebars.secretDetails.saveError'),
        ),
      )
    } finally {
      setIsProcessing(false)
    }
  }, [
    secretId,
    secret,
    name,
    key,
    tags,
    newValue,
    valueCleared,
    makeSensitive,
    t,
  ])

  const handleDelete = useCallback(async () => {
    setIsDeleteConfirmOpen(false)
    setFormError(null)
    try {
      setIsProcessing(true)
      await secretsModel.deleteSecretFx(secretId)
      onClose()
    } catch (error) {
      setFormError(
        extractApiErrorMessage(
          error,
          t('widgets.sidebars.secretDetails.deleteError'),
        ),
      )
    } finally {
      setIsProcessing(false)
    }
  }, [secretId, onClose, t])

  if (!secret) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        <div className="text-neutral-4">
          {t('widgets.sidebars.secret.loading')}
        </div>
      </div>
    )
  }

  const isVariable = !secret.sensitive

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
      <SidebarCloseButton onClose={onClose} />

      <h2 className="text-lg font-bold uppercase">
        {isVariable
          ? t('widgets.sidebars.secretDetails.variableTitle')
          : t('widgets.sidebars.secretDetails.title')}
      </h2>

      <div className="flex flex-col gap-2">
        <label className="text-sm font-medium text-neutral-7 uppercase">
          {t('widgets.sidebars.secret.kindLabel')}
        </label>
        <div className="text-sm text-neutral-2 uppercase">
          {t(kindLabelKey[secret.kind])}
          {isVariable && (
            <span className="ml-2 border border-blue-7 text-blue-5 rounded-[3px] px-1.5 py-px text-xxs uppercase">
              {t('entities.secret.variable')}
            </span>
          )}
        </div>
      </div>

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <Input
        type="text"
        name="name"
        label={t('widgets.sidebars.secret.nameLabel')}
        value={name}
        onChange={(e) => setName(e.target.value)}
        disabled={isProcessing}
      />

      <SecretTagsInput
        value={tags}
        onChange={setTags}
        disabled={isProcessing}
      />

      <Input
        type="text"
        name="key"
        label={t(keyLabelKey[secret.kind])}
        value={key}
        onChange={(e) => setKey(e.target.value)}
        disabled={isProcessing}
      />

      {isVariable ? (
        <>
          <Input
            type="text"
            name="value"
            label={t('widgets.sidebars.secret.valueLabel')}
            value={newValue}
            onChange={(e) => setNewValue(e.target.value)}
            disabled={isProcessing}
          />
          <label className="flex items-start gap-2 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={makeSensitive}
              onChange={(e) => setMakeSensitive(e.target.checked)}
              disabled={isProcessing}
              className="mt-0.5 accent-lime-600"
            />
            <span className="flex flex-col gap-0.5">
              <span className="text-sm text-neutral-2">
                {t('widgets.sidebars.secret.makeSensitive')}
              </span>
              <span className="text-xs text-neutral-5">
                {t('widgets.sidebars.secret.makeSensitiveHint')}
              </span>
            </span>
          </label>
        </>
      ) : (
        <div className="flex flex-col gap-2">
          <Input
            type="password"
            name="value"
            label={t('widgets.sidebars.secret.valueLabel')}
            placeholder={
              valueCleared
                ? t('widgets.sidebars.secret.valueEmptyPlaceholder')
                : secret.has_value
                  ? t('widgets.sidebars.secret.valueSet')
                  : t('widgets.sidebars.secret.valueEmptyPlaceholder')
            }
            value={newValue}
            onChange={(e) => {
              setNewValue(e.target.value)
              setValueCleared(false)
            }}
            disabled={isProcessing}
          />
          {(secret.has_value || newValue) && (
            <button
              type="button"
              onClick={() => {
                setNewValue('')
                setValueCleared(true)
              }}
              className="self-start text-xs text-neutral-5 hover:text-neutral-2 uppercase"
            >
              {valueCleared
                ? t('widgets.sidebars.secret.valueWillClear')
                : t('widgets.sidebars.secret.clearValue')}
            </button>
          )}
        </div>
      )}

      <SubjectAttachmentTargets subject={{ kind: 'secret', secretId }} />

      <div className="flex flex-col gap-3 pt-4">
        {canManage && (
          <>
            <Button size="lg" onClick={handleSave} disabled={isProcessing} grow>
              {isProcessing
                ? t('widgets.sidebars.secretDetails.saving')
                : t('widgets.sidebars.secretDetails.save')}
            </Button>
            <Button
              size="lg"
              onClick={() => setIsDeleteConfirmOpen(true)}
              disabled={isProcessing}
              variant="destructive"
              grow
            >
              {t('widgets.sidebars.secretDetails.delete')}
            </Button>
          </>
        )}
      </div>
      <ConfirmDialog
        open={isDeleteConfirmOpen}
        title={t('widgets.sidebars.secretDetails.delete')}
        message={t('widgets.sidebars.secretDetails.deleteConfirm')}
        confirmText={t('widgets.sidebars.secretDetails.delete')}
        destructive
        onConfirm={handleDelete}
        onCancel={() => setIsDeleteConfirmOpen(false)}
      />
    </div>
  )
}
