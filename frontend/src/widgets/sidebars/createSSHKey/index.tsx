import { getRouteApi } from '@tanstack/react-router'
import { FC, useState, type SyntheticEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { apiSSHKeys, sshKeysModel } from '@/entities/sshKey'
import type { SSHKeyResult, SSHKeyType } from '@/entities/sshKey/api/types'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarCloseButton } from '../closeButton'
import { SidebarProps } from '../types'

const layoutApi = getRouteApi('/_mainLayout')

type Mode = 'paste' | 'generate'

export const SidebarCreateSSHKey: FC<SidebarProps> = ({ onClose }) => {
  const { t } = useTranslation()
  // The key type is implied by the tab this sidebar was opened from — the CA
  // Certificates tab creates CA keys, everything else creates SSH keys — so the
  // operator does not pick it again. Defaults to 'ssh' outside the credentials
  // tabs.
  const layoutSearch = layoutApi.useSearch()
  const keyType: SSHKeyType = layoutSearch.tab === 'ca' ? 'ca' : 'ssh'
  const isCA = keyType === 'ca'

  const [mode, setMode] = useState<Mode>('paste')
  const [isProcessing, setIsProcessing] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)
  const [alias, setAlias] = useState('')
  const [keyContent, setKeyContent] = useState('')
  const [generated, setGenerated] = useState<SSHKeyResult | null>(null)
  const [publicKeyText, setPublicKeyText] = useState<string>('')

  const handleFormSubmit = async (
    event: SyntheticEvent<HTMLFormElement, SubmitEvent>,
  ) => {
    event.preventDefault()
    setFormError(null)

    if (!alias.trim()) {
      setFormError(t('widgets.sidebars.createSSHKey.aliasRequired'))
      return
    }
    if (mode === 'paste' && !keyContent.trim()) {
      setFormError(t('widgets.sidebars.createSSHKey.keyContentRequired'))
      return
    }

    setIsProcessing(true)
    try {
      if (mode === 'generate') {
        const key = await apiSSHKeys.generateSSHKey({
          alias: alias.trim(),
        })
        await sshKeysModel.fetchSSHKeysFx()
        // Fetch the public part so the operator can copy/download it right away.
        const pub = await apiSSHKeys
          .getSSHKeyPublicKeyText(key.id)
          .catch(() => '')
        setPublicKeyText(pub)
        setGenerated(key)
      } else {
        await apiSSHKeys.postCreateSSHKey({
          alias: alias.trim(),
          key_type: keyType,
          key_content: keyContent.trim(),
        })
        await sshKeysModel.fetchSSHKeysFx()
        onClose()
      }
    } catch (error: unknown) {
      setFormError(
        extractApiErrorMessage(
          error,
          t('widgets.sidebars.createSSHKey.createError'),
        ),
      )
    } finally {
      setIsProcessing(false)
    }
  }

  const handleDownload = () => {
    if (generated) {
      void apiSSHKeys.downloadSSHKeyPublicKey(generated.id)
    }
  }

  // Post-generate success view: the private key stays server-side; expose the
  // public part to deploy onto devices with password auth disabled.
  if (generated) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-5">
        <SidebarCloseButton onClose={onClose} />
        <h2 className="text-lg font-bold uppercase">
          {t('widgets.sidebars.createSSHKey.generatedTitle')}
        </h2>
        <div className="bg-lime-6/10 border border-lime-6/50 text-lime-5 px-4 py-3 rounded text-sm">
          {t('widgets.sidebars.createSSHKey.generatedNoticeIntro')}{' '}
          <span className="font-bold">{generated.alias}</span>{' '}
          {t('widgets.sidebars.createSSHKey.generatedNoticeSuffix')}
        </div>
        {publicKeyText && (
          <textarea
            readOnly
            value={publicKeyText.trim()}
            className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-xs text-neutral-2 font-mono min-h-[80px] resize-y"
          />
        )}
        <div className="flex gap-3">
          <Button size="md" onClick={handleDownload}>
            {t('widgets.sidebars.createSSHKey.downloadPublicKey')}
          </Button>
          <Button size="md" kind="secondary" onClick={onClose}>
            {t('widgets.sidebars.createSSHKey.done')}
          </Button>
        </div>
      </div>
    )
  }

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
      <SidebarCloseButton onClose={onClose} />

      <h2 className="text-lg font-bold uppercase">
        {isCA
          ? t('widgets.sidebars.createSSHKey.titleCA')
          : t('widgets.sidebars.createSSHKey.titleSSH')}
      </h2>

      {/* Mode toggle. Generating a keypair produces an SSH connection key, which
          is meaningless for a CA — a CA is your organization's, pasted in — so
          the toggle is hidden on the CA tab. */}
      {!isCA && (
        <div className="flex gap-1 border-b border-neutral-11">
          <button
            type="button"
            onClick={() => setMode('paste')}
            className={
              'px-3 py-2 text-xs uppercase tracking-wide border-b-2 -mb-px transition-colors ' +
              (mode === 'paste'
                ? 'border-lime-6 text-lime-6'
                : 'border-transparent text-neutral-7 hover:text-neutral-5')
            }
          >
            {t('widgets.sidebars.createSSHKey.modePaste')}
          </button>
          <button
            type="button"
            onClick={() => setMode('generate')}
            className={
              'px-3 py-2 text-xs uppercase tracking-wide border-b-2 -mb-px transition-colors ' +
              (mode === 'generate'
                ? 'border-lime-6 text-lime-6'
                : 'border-transparent text-neutral-7 hover:text-neutral-5')
            }
          >
            {t('widgets.sidebars.createSSHKey.modeGenerate')}
          </button>
        </div>
      )}

      {formError && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {formError}
        </div>
      )}

      <form onSubmit={handleFormSubmit} className="flex flex-col gap-6">
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.createSSHKey.aliasLabel')}
          </label>
          <Input
            type="text"
            name="alias"
            value={alias}
            onChange={(e) => setAlias(e.target.value)}
            placeholder={t('widgets.sidebars.createSSHKey.aliasPlaceholder')}
            required
            disabled={isProcessing}
          />
        </div>

        {mode === 'generate' ? (
          <div className="text-xs text-neutral-4">
            {t('widgets.sidebars.createSSHKey.generateHint')}
          </div>
        ) : (
          <>
            <div className="flex flex-col gap-2">
              <label className="text-sm font-medium text-neutral-7 uppercase">
                {keyType === 'ca'
                  ? t('widgets.sidebars.createSSHKey.publicKeyLabelCA')
                  : t('widgets.sidebars.createSSHKey.publicKeyLabel')}
              </label>
              <textarea
                name="key_content"
                value={keyContent}
                onChange={(e) => setKeyContent(e.target.value)}
                placeholder={
                  keyType === 'ca'
                    ? t('widgets.sidebars.createSSHKey.publicKeyPlaceholderCA')
                    : t('widgets.sidebars.createSSHKey.publicKeyPlaceholder')
                }
                className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5 font-mono min-h-[120px] resize-y"
                disabled={isProcessing}
              />
              <div className="text-xs text-neutral-4">
                {keyType === 'ca'
                  ? t('widgets.sidebars.createSSHKey.publicKeyHintCA')
                  : t('widgets.sidebars.createSSHKey.publicKeyHint')}
              </div>
            </div>
          </>
        )}

        <Button type="submit" size="lg" disabled={isProcessing} grow>
          {isProcessing
            ? mode === 'generate'
              ? t('widgets.sidebars.createSSHKey.generating')
              : t('widgets.sidebars.createSSHKey.creating')
            : mode === 'generate'
              ? t('widgets.sidebars.createSSHKey.submitGenerate')
              : t('widgets.sidebars.createSSHKey.submitCreate')}
        </Button>
      </form>
    </div>
  )
}
