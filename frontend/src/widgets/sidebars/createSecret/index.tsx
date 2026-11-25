import { FC, useState, type SyntheticEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { secretsModel, type SecretKind } from '@/entities/secret'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { hasLineBreak, isEnvKey, isSecretName } from '@/shared/utils/formRules'

import { SidebarCloseButton } from '../closeButton'
import { SecretTagsInput } from '../secretTagsInput'
import { SidebarProps } from '../types'

const selectClassName =
  'w-full bg-neutral-13 rounded px-2.5 py-2.5 text-white outline-none transition-colors disabled:opacity-50 disabled:cursor-not-allowed'

// The `key` field label depends on the selected kind.
const keyLabelKey = {
  env: 'widgets.sidebars.secret.keyEnv',
  credential: 'widgets.sidebars.secret.keyCredential',
  opaque: 'widgets.sidebars.secret.keyOpaque',
} as const satisfies Record<SecretKind, string>

export const SidebarCreateSecret: FC<SidebarProps> = ({ onClose }) => {
  const { t } = useTranslation()
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [kind, setKind] = useState<SecretKind>('env')
  const [key, setKey] = useState('')
  const [value, setValue] = useState('')
  // Only env entries may be non-sensitive ("variables", value stays readable).
  const [sensitive, setSensitive] = useState(true)
  const [tags, setTags] = useState<string[]>([])

  const handleFormSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setFormError(null)

    if (!name.trim()) {
      setFormError(t('widgets.sidebars.secret.nameRequired'))
      return
    }
    if (!isSecretName(name)) {
      setFormError(t('widgets.sidebars.secret.nameInvalid'))
      return
    }
    if (kind === 'env' && !isEnvKey(key.trim())) {
      setFormError(t('widgets.sidebars.secret.envKeyInvalid'))
      return
    }
    if (kind === 'env' && hasLineBreak(value)) {
      setFormError(t('widgets.sidebars.secret.envValueLineBreak'))
      return
    }

    setIsProcessing(true)

    try {
      await secretsModel.createSecretFx({
        name: name.trim(),
        kind,
        key: key.trim(),
        value,
        sensitive: kind === 'env' ? sensitive : true,
        tags,
      })
      onClose()
    } catch (error) {
      setFormError(
        extractApiErrorMessage(error, t('widgets.sidebars.createSecret.error')),
      )
    } finally {
      setIsProcessing(false)
    }
  }

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
      <SidebarCloseButton onClose={onClose} />

      <h2 className="text-lg font-bold uppercase">
        {t('widgets.sidebars.createSecret.title')}
      </h2>

      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.createSecret.description')}
      </div>

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <form onSubmit={handleFormSubmit} className="flex flex-col gap-6">
        <Input
          type="text"
          name="name"
          label={t('widgets.sidebars.secret.nameLabel')}
          placeholder={t('widgets.sidebars.secret.namePlaceholder')}
          value={name}
          onChange={(e) => setName(e.target.value)}
          required
          disabled={isProcessing}
        />

        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.secret.kindLabel')}
          </label>
          <select
            name="kind"
            value={kind}
            onChange={(e) => setKind(e.target.value as SecretKind)}
            disabled={isProcessing}
            className={selectClassName}
          >
            <option value="env">{t('entities.secret.kind.env')}</option>
            <option value="credential">
              {t('entities.secret.kind.credential')}
            </option>
            <option value="opaque">{t('entities.secret.kind.opaque')}</option>
          </select>
        </div>

        {kind === 'env' && (
          <label className="flex items-start gap-2 cursor-pointer select-none">
            <input
              type="checkbox"
              checked={!sensitive}
              onChange={(e) => setSensitive(!e.target.checked)}
              disabled={isProcessing}
              className="mt-0.5 accent-lime-600"
            />
            <span className="flex flex-col gap-0.5">
              <span className="text-sm text-neutral-2">
                {t('widgets.sidebars.secret.variableToggle')}
              </span>
              <span className="text-xs text-neutral-5">
                {t('widgets.sidebars.secret.variableToggleHint')}
              </span>
            </span>
          </label>
        )}

        <SecretTagsInput
          value={tags}
          onChange={setTags}
          disabled={isProcessing}
        />

        <Input
          type="text"
          name="key"
          label={t(keyLabelKey[kind])}
          value={key}
          onChange={(e) => setKey(e.target.value)}
          disabled={isProcessing}
        />

        <Input
          type={kind === 'env' && !sensitive ? 'text' : 'password'}
          name="value"
          label={t('widgets.sidebars.secret.valueLabel')}
          placeholder={t('widgets.sidebars.secret.valuePlaceholder')}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          disabled={isProcessing}
        />

        <Button type="submit" size="lg" disabled={isProcessing} grow>
          {isProcessing
            ? t('widgets.sidebars.createSecret.creating')
            : t('widgets.sidebars.createSecret.create')}
        </Button>
      </form>
    </div>
  )
}
