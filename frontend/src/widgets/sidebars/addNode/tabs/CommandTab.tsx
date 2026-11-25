import { FC, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { postCreateMasterToken } from '@/entities/masterToken/api'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'
import { useCopyText } from '@/shared/utils/copyText'
import { buildInstallCommand } from '@/shared/utils/installCommand'

import { consoleLoginError, ConsoleLoginFields } from '../ConsoleLoginFields'

// CommandTab: one-command adoption — the path every install offers. The user
// runs a single command on the device; it downloads the agent, writes the
// adoption file and starts the service; the agent dials OUT over gRPC-TLS, so
// it works from behind any NAT. The command embeds a freshly minted master token
// (7-day expiry server-side), so devices land as pending-approval nodes.
export const CommandTab: FC = () => {
  const { t } = useTranslation()
  const [tokenName, setTokenName] = useState('')
  const [defaultUsername, setDefaultUsername] = useState('')
  const [defaultPassword, setDefaultPassword] = useState('')
  const [command, setCommand] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const { state: copyState, copy } = useCopyText()
  const [error, setError] = useState<string | null>(null)

  const handleGenerate = async () => {
    setError(null)
    setCommand(null)
    const credentialError = consoleLoginError(defaultUsername, defaultPassword)
    if (credentialError) {
      setError(credentialError)
      return
    }
    setBusy(true)
    try {
      const token = await postCreateMasterToken({
        name:
          tokenName.trim() ||
          `one-command adopt ${new Date().toISOString().slice(0, 10)}`,
        default_username: defaultUsername.trim(),
        default_password: defaultPassword,
      })
      if (!token.adoption_file) {
        setError(t('widgets.sidebars.addNode.command.noAdoptionToken'))
        return
      }
      setCommand(buildInstallCommand(token.adoption_file))
    } catch (e: unknown) {
      setError(
        extractApiErrorMessage(
          e,
          t('widgets.sidebars.addNode.command.createTokenError'),
        ),
      )
    } finally {
      setBusy(false)
    }
  }

  const handleCopy = () => {
    if (!command) return
    void copy(command)
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="text-sm text-neutral-4">
        {t('widgets.sidebars.addNode.command.description')}
      </div>

      {error && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {error}
        </div>
      )}

      <Input
        label={t('widgets.sidebars.addNode.command.tokenNameLabel')}
        value={tokenName}
        onChange={(e) => setTokenName(e.target.value)}
        placeholder={t('widgets.sidebars.addNode.command.tokenNamePlaceholder')}
        disabled={busy}
      />
      <ConsoleLoginFields
        timing="on-connect"
        username={defaultUsername}
        password={defaultPassword}
        onUsernameChange={setDefaultUsername}
        onPasswordChange={setDefaultPassword}
        disabled={busy}
      />

      <Button size="lg" onClick={handleGenerate} disabled={busy} grow>
        {busy
          ? t('widgets.sidebars.addNode.command.generating')
          : command
            ? t('widgets.sidebars.addNode.command.generateNew')
            : t('widgets.sidebars.addNode.command.generate')}
      </Button>

      {command && (
        <div className="flex flex-col gap-2">
          <div className="text-xs uppercase font-medium text-neutral-6">
            {t('widgets.sidebars.addNode.command.runOnDevice')}
          </div>
          <pre className="bg-neutral-13 rounded px-3 py-2 text-xs text-neutral-3 font-mono whitespace-pre-wrap break-all select-all">
            {command}
          </pre>
          <Button size="md" kind="secondary" onClick={handleCopy} grow>
            {copyState === 'copied'
              ? t('widgets.sidebars.addNode.command.copied')
              : copyState === 'failed'
                ? t('shared.copy.manual')
                : t('widgets.sidebars.addNode.command.copy')}
          </Button>
          <div className="text-xs text-neutral-6">
            {t('widgets.sidebars.addNode.command.tokenNote')}
          </div>
        </div>
      )}
    </div>
  )
}
