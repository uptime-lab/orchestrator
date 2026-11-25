import { t } from 'i18next'
import { useEffect, useState } from 'react'

import { apiAuth } from '@/entities/auth/api'
import type { Passkey } from '@/entities/auth/api/passkey'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

// PasskeysSection manages the signed-in user's WebAuthn credentials.
//
// Hidden on browsers without WebAuthn rather than shown broken: there is
// nothing useful a user can do here without it.
export const PasskeysSection = () => {
  const [items, setItems] = useState<Passkey[] | null>(null)
  const [name, setName] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Removing a passkey asks first: if it is the only second factor in an
  // account that requires one, the next sign-in starts with enrolment.
  const [removing, setRemoving] = useState<Passkey | null>(null)

  const refresh = () => {
    apiAuth
      .listPasskeys()
      .then(setItems)
      .catch(() => setItems([]))
  }

  useEffect(refresh, [])

  if (!apiAuth.isPasskeySupported() || items === null) return null

  const add = async () => {
    setError(null)
    setPending(true)
    try {
      await apiAuth.registerPasskey(
        name.trim() || t('widgets.passkeysSection.defaultName'),
      )
      setName('')
      refresh()
    } catch (e: unknown) {
      // Dismissing the browser prompt is a decision, not a failure.
      if (!(e instanceof DOMException && e.name === 'NotAllowedError')) {
        setError(extractApiErrorMessage(e, t('pages.account.passkeys.error')))
      }
    } finally {
      setPending(false)
    }
  }

  const remove = async (id: string) => {
    setError(null)
    try {
      await apiAuth.deletePasskey(id)
      refresh()
    } catch (e: unknown) {
      setError(extractApiErrorMessage(e, t('pages.account.passkeys.error')))
    }
  }

  return (
    <div className="flex flex-col gap-3 border border-neutral-10 rounded p-4 mb-4">
      <div className="text-sm uppercase font-bold">
        {t('pages.account.passkeys.title')}
      </div>
      <p className="text-sm text-neutral-7">
        {t('pages.account.passkeys.description')}
      </p>

      {items.length > 0 && (
        <div className="flex flex-col gap-2">
          {items.map((pk) => (
            <div
              key={pk.id}
              className="flex items-center gap-4 border border-neutral-10 rounded px-3 py-2"
            >
              <div className="min-w-0 flex-1">
                <div className="text-sm text-neutral-5 truncate">{pk.name}</div>
                <div className="text-xs text-neutral-7">
                  {pk.synced
                    ? t('pages.account.passkeys.synced')
                    : t('pages.account.passkeys.deviceBound')}
                  {pk.last_used_at
                    ? ` · ${t('pages.account.passkeys.lastUsed', {
                        when: new Date(pk.last_used_at).toLocaleDateString(),
                      })}`
                    : ` · ${t('pages.account.passkeys.neverUsed')}`}
                </div>
              </div>
              <Button
                size="sm"
                variant="destructive"
                onClick={() => setRemoving(pk)}
              >
                {t('pages.account.passkeys.remove')}
              </Button>
            </div>
          ))}
        </div>
      )}

      <div className="flex flex-wrap items-end gap-3">
        <div className="flex-1 min-w-[220px]">
          <Input
            id="passkey-name"
            name="passkey-name"
            label={t('pages.account.passkeys.name')}
            placeholder={t('pages.account.passkeys.namePlaceholder')}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <Button size="lg" disabled={pending} onClick={add}>
          {t('pages.account.passkeys.add')}
        </Button>
      </div>

      {error && <div className="text-sm text-red-9">{error}</div>}
      <ConfirmDialog
        open={removing !== null}
        title={t('pages.account.passkeys.removeTitle')}
        message={t('pages.account.passkeys.removeMessage', {
          name: removing?.name ?? '',
        })}
        confirmText={t('pages.account.passkeys.remove')}
        destructive
        busy={pending}
        onConfirm={() => {
          const target = removing
          setRemoving(null)
          if (target) void remove(target.id)
        }}
        onCancel={() => setRemoving(null)}
      />
    </div>
  )
}
