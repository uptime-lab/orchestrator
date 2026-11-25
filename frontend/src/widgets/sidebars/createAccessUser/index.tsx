import { getRouteApi, useNavigate } from '@tanstack/react-router'
import { FC, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  ACCESS_ACCOUNT_WIDE_FILTER,
  accessChanged,
  apiAccess,
} from '@/entities/access'
import { useOUs } from '@/entities/ou'
import { useSSHKeys } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarCloseButton } from '../closeButton'
import { SidebarProps } from '../types'

const layoutApi = getRouteApi('/_mainLayout')

// Create a named OS user in the scope currently selected in the credentials
// rail. Mirrors the other create sidebars (a right-hand panel opened from a
// tab's "Add" button), so the interaction matches nodes / SSH keys / tokens.
export const SidebarCreateAccessUser: FC<SidebarProps> = ({ onClose }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { ous } = useOUs()
  const { keys } = useSSHKeys()
  const search = layoutApi.useSearch()

  // The scope is taken from the rail selection — no selector here: a unit
  // picked in the rail creates the user there, "All nodes" creates a
  // fleet-wide default, anything else lands in Unassigned (applies to no
  // node — the safe default). Memberships are managed afterwards by dragging
  // the user's row onto units.
  const ACCOUNT_WIDE = ACCESS_ACCOUNT_WIDE_FILTER
  const railOU = ous.find((o) => o.id === search.ou)
  const scopeOUIds = railOU ? [railOU.id] : []
  const fleetWide = search.ou === ACCOUNT_WIDE

  const [username, setUsername] = useState('')
  // Off by default, as in the API and the attach dialog: a user created
  // without a password would otherwise get password-less sudo.
  const [sudo, setSudo] = useState(false)
  const [password, setPassword] = useState('')
  const [principals, setPrincipals] = useState('')
  const [keyIds, setKeyIds] = useState<string[]>([])
  const [pendingKey, setPendingKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [formError, setFormError] = useState<string | null>(null)

  const sshKeys = keys.filter((k) => k.key_type !== 'ca')
  const attachable = sshKeys.filter((k) => !keyIds.includes(k.id))
  const keyLabel = (id: string) => {
    const k = keys.find((x) => x.id === id)
    return k ? k.alias || k.fingerprint || id : id
  }

  const addPendingKey = () => {
    if (pendingKey && !keyIds.includes(pendingKey)) {
      setKeyIds((prev) => [...prev, pendingKey])
    }
    setPendingKey('')
  }

  const submit = async () => {
    setBusy(true)
    setFormError(null)
    try {
      await apiAccess.createAccessEntry({
        type: 'user',
        ou_ids: fleetWide ? undefined : scopeOUIds,
        account_wide: fleetWide || undefined,
        username: username.trim(),
        sudo,
        password: password || undefined,
        key_ids: keyIds,
        principals: principals
          .split(',')
          .map((p) => p.trim())
          .filter(Boolean),
      })
      accessChanged()
      // One navigation closing the sidebar AND moving the rail to the created
      // scope — otherwise the new user lands behind the current filter and
      // looks like it was never created. (Not onClose(): a second navigate for
      // `ou` would race the close's own search update.)
      navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({
          ...prev,
          ou: fleetWide ? ACCOUNT_WIDE : scopeOUIds[0] || undefined,
          sidebar: undefined,
        }),
      })
    } catch (e) {
      setFormError(
        extractApiErrorMessage(
          e,
          t('widgets.sidebars.createAccessUser.createError'),
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
        {t('pages.credentials.userSidebar.addTitle')}
      </h2>
      <div className="flex flex-col gap-4">
        {/* Where the user will land — driven by the rail selection, no
            selector: assignment afterwards is drag-onto-a-unit. */}
        <p className="text-xs text-neutral-6">
          {t('pages.credentials.userSidebar.willCreateIn', {
            scope: fleetWide
              ? t('pages.credentials.accountWide')
              : (railOU?.name ?? t('pages.credentials.unassigned')),
          })}
        </p>

        <Input
          type="text"
          label={t('pages.credentials.userSidebar.username')}
          placeholder={t(
            'widgets.sidebars.createAccessUser.usernamePlaceholder',
          )}
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          disabled={busy}
        />

        <label className="flex items-center gap-2 text-sm text-neutral-4">
          <input
            type="checkbox"
            checked={sudo}
            disabled={busy}
            onChange={(e) => setSudo(e.target.checked)}
          />
          {t('pages.credentials.userSidebar.sudo')}
        </label>

        <Input
          type="password"
          label={t('pages.credentials.userSidebar.password')}
          placeholder={t('pages.credentials.userSidebar.passwordPlaceholder')}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={busy}
        />

        <div className="flex flex-col gap-1.5">
          <Input
            type="text"
            label={t('pages.credentials.userSidebar.principals')}
            placeholder={t(
              'pages.credentials.userSidebar.principalsPlaceholder',
            )}
            value={principals}
            onChange={(e) => setPrincipals(e.target.value)}
            disabled={busy}
          />
          <p className="text-xs text-neutral-6">
            {t('pages.credentials.userSidebar.principalsHint')}
          </p>
        </div>

        <div className="flex flex-col gap-2">
          <div className="text-sm font-medium text-neutral-7 uppercase">
            {t('pages.credentials.userSidebar.keys')}
          </div>
          {sshKeys.length === 0 ? (
            <p className="text-xs text-neutral-6">
              {t('pages.credentials.userSidebar.noKeys')}
            </p>
          ) : (
            <>
              {/* Selected keys as removable chips. */}
              {keyIds.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {keyIds.map((id) => (
                    <span
                      key={id}
                      className="flex items-center gap-1 rounded bg-neutral-12 px-2 py-1 text-xs text-neutral-2"
                    >
                      {keyLabel(id)}
                      <button
                        type="button"
                        disabled={busy}
                        onClick={() =>
                          setKeyIds((prev) => prev.filter((k) => k !== id))
                        }
                        className="text-neutral-7 hover:text-status-error"
                        aria-label={t(
                          'widgets.sidebars.createAccessUser.removeKeyAria',
                        )}
                      >
                        ×
                      </button>
                    </span>
                  ))}
                </div>
              )}
              {/* Dropdown to add a key. */}
              <div className="flex items-center gap-2">
                <select
                  value={pendingKey}
                  disabled={busy || attachable.length === 0}
                  onChange={(e) => setPendingKey(e.target.value)}
                  className="flex-1 rounded border border-neutral-11 bg-neutral-13 px-2.5 py-2 text-sm text-neutral-2 outline-none disabled:opacity-50"
                >
                  <option value="">
                    {attachable.length === 0
                      ? t('pages.credentials.access.noMoreKeys')
                      : t('pages.credentials.access.attachKeyPlaceholder')}
                  </option>
                  {attachable.map((k) => (
                    <option key={k.id} value={k.id}>
                      {k.alias || k.fingerprint}
                    </option>
                  ))}
                </select>
                <button
                  type="button"
                  disabled={busy || !pendingKey}
                  onClick={addPendingKey}
                  className="text-lime-6 text-sm hover:underline disabled:opacity-40"
                >
                  {t('pages.credentials.access.attach')}
                </button>
              </div>
            </>
          )}
          <p className="text-xs text-neutral-6">
            {t('pages.credentials.userSidebar.keysHint')}
          </p>
        </div>

        {formError ? (
          <div className="text-status-error text-sm">{formError}</div>
        ) : null}

        <Button onClick={submit} disabled={busy || !username.trim()}>
          {t('pages.credentials.userSidebar.create')}
        </Button>
      </div>
    </div>
  )
}
