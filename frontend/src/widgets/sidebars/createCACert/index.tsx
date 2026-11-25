import { FC, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  accessChanged,
  nameFromComment,
  parseCertAuthorityLine,
} from '@/entities/access'
import { apiSSHKeys, sshKeysModel } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarCloseButton } from '../closeButton'
import { SidebarProps } from '../types'

// Registers a CA in the inventory: paste a full cert-authority line or a bare CA
// public key. It creates only the key — it does NOT trust it on any unit. Trust
// (and the principals, which can differ per unit) is set when you drag the CA
// onto a unit. If the pasted line carried principals, they are shown here as a
// reminder to set at trust time.
export const SidebarCreateCACert: FC<SidebarProps> = ({ onClose }) => {
  const { t } = useTranslation()
  const [raw, setRaw] = useState('')
  const [alias, setAlias] = useState('')
  const [busy, setBusy] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  // Parse whatever is in the box. Works for a full cert-authority line or a bare
  // key; the key content is normalized (options stripped) either way.
  const parsed = useMemo(() => parseCertAuthorityLine(raw), [raw])

  const submit = async () => {
    setBusy(true)
    setFormError(null)
    try {
      if (!parsed) {
        setFormError(t('widgets.sidebars.createCACert.parseError'))
        return
      }
      await apiSSHKeys.postCreateSSHKey({
        alias: alias.trim() || nameFromComment(parsed.comment),
        key_type: 'ca',
        key_content: parsed.keyContent,
      })
      await sshKeysModel.fetchSSHKeysFx()
      accessChanged()
      onClose()
    } catch (e) {
      setFormError(
        extractApiErrorMessage(
          e,
          t('widgets.sidebars.createCACert.createError'),
        ),
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-5">
      <SidebarCloseButton onClose={onClose} />
      <h2 className="text-lg font-bold uppercase text-neutral-1">
        {t('widgets.sidebars.createCACert.title')}
      </h2>
      <div className="flex flex-col gap-4">
        <div className="flex flex-col gap-2">
          <label className="text-sm font-medium text-neutral-7 uppercase">
            {t('widgets.sidebars.createCACert.caLabel')}
          </label>
          <textarea
            value={raw}
            onChange={(e) => setRaw(e.target.value)}
            disabled={busy}
            placeholder={t('widgets.sidebars.createCACert.caPlaceholder')}
            className="min-h-[110px] w-full resize-y rounded border border-neutral-11 bg-neutral-13 px-2.5 py-2 font-mono text-xs text-neutral-2 outline-none disabled:opacity-50"
          />
          <p className="text-xs text-neutral-6">
            {t('widgets.sidebars.createCACert.caHint')}
          </p>
        </div>

        <Input
          type="text"
          label={t('widgets.sidebars.createCACert.aliasLabel')}
          placeholder={
            parsed?.comment
              ? nameFromComment(parsed.comment)
              : t('widgets.sidebars.createCACert.aliasPlaceholder')
          }
          value={alias}
          onChange={(e) => setAlias(e.target.value)}
          disabled={busy}
        />

        <p className="text-xs text-neutral-6">
          {t('widgets.sidebars.createCACert.trustNote')}
        </p>

        {formError ? (
          <div className="text-status-error text-sm">{formError}</div>
        ) : null}

        <Button onClick={submit} disabled={busy || !raw.trim()}>
          {t('widgets.sidebars.createCACert.create')}
        </Button>
      </div>
    </div>
  )
}
