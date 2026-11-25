import { useNavigate } from '@tanstack/react-router'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  accessChanged,
  apiAccess,
  editAccessEntry,
  lockReasonText,
  withPrincipalAdded,
  withPrincipalRemoved,
  withUnitRemoved,
  type AccessEntry,
} from '@/entities/access'
import { useNode } from '@/entities/node/hooks.ts'
import { useOUs } from '@/entities/ou'
import { useSSHKeys } from '@/entities/sshKey'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarCloseButton } from '../closeButton'
import { SidebarEnum, SidebarProps } from '../types'

// Edit one access user: its keys (attach/detach individually), sudo, password,
// and delete. Keys are managed per-key, never as a replaced set, so a stale
// view cannot wipe a key it never saw.
export const SidebarAccessUserDetails: FC<
  SidebarProps & { entryId: string }
> = ({ entryId, onClose }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { keys } = useSSHKeys()
  const { ous } = useOUs()
  const [entry, setEntry] = useState<AccessEntry | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [attachId, setAttachId] = useState('')
  const [password, setPassword] = useState('')
  const [pendingPrincipal, setPendingPrincipal] = useState('')
  const [confirmDelete, setConfirmDelete] = useState(false)
  // Resolve the node a node-scoped entry is bound to (no-op for other scopes).
  const { node: boundNode } = useNode({ nodeId: entry?.node_id ?? '' })

  const load = useCallback(async () => {
    try {
      setEntry(await apiAccess.getAccessEntry(entryId))
    } catch {
      setError(t('pages.credentials.userSidebar.loadError'))
    }
  }, [entryId, t])

  useEffect(() => {
    void load()
  }, [load])

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    setError(null)
    try {
      await fn()
      await load()
      accessChanged()
    } catch (e) {
      setError(
        extractApiErrorMessage(
          e,
          t('widgets.sidebars.accessUserDetails.requestFailed'),
        ),
      )
    } finally {
      setBusy(false)
    }
  }

  const keyLabel = (id: string) => {
    const k = keys.find((x) => x.id === id)
    return k ? k.alias || k.fingerprint || id : id
  }

  if (!entry) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-5">
        <SidebarCloseButton onClose={onClose} />
        <div className="text-sm text-neutral-6">
          {error ?? t('pages.credentials.userSidebar.loading')}
        </div>
      </div>
    )
  }

  const attachable = keys.filter(
    (k) => k.key_type !== 'ca' && !entry.key_ids.includes(k.id),
  )
  const lockText = lockReasonText(entry)

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-5">
      <SidebarCloseButton onClose={onClose} />
      <div>
        <h2 className="text-lg font-bold uppercase text-neutral-1">
          {entry.username}
        </h2>
        <div className="text-xs text-neutral-7">
          {entry.lifecycle === 'locked'
            ? t('pages.credentials.access.locked')
            : t('pages.credentials.access.active')}
        </div>
      </div>
      <div className="flex flex-col gap-4">
        {lockText ? (
          <div className="text-status-warning text-xs">{lockText}</div>
        ) : null}

        {/* Memberships: chips with per-unit removal. ADDING is done by
            dragging the user's row onto a unit in the rail (or onto "All
            nodes"), so there is no selector here. A node-scoped entry is
            edited from the node. */}
        <div className="flex flex-col gap-2">
          <div className="text-sm font-medium text-neutral-7 uppercase">
            {entry.scope === 'node'
              ? t('pages.credentials.userSidebar.nodeScopeLabel')
              : t('pages.credentials.userSidebar.scopeLabel')}
          </div>
          {entry.scope === 'node' ? (
            <div className="flex flex-col gap-2">
              <div className="flex items-center justify-between gap-2 text-sm">
                <button
                  type="button"
                  onClick={() =>
                    navigate({
                      to: '/',
                      search: {
                        sidebar: SidebarEnum.Node,
                        nodeId: entry.node_id,
                        view: 'properties',
                      } as never,
                    })
                  }
                  className="min-w-0 truncate text-left text-lime-6 hover:underline"
                >
                  {boundNode?.name || boundNode?.hostname || entry.node_id}
                </button>
                <button
                  type="button"
                  disabled={busy}
                  onClick={() =>
                    run(() =>
                      apiAccess.updateAccessEntry(entry.id, { scope: {} }),
                    )
                  }
                  className="flex-shrink-0 text-xs text-neutral-7 hover:text-status-error"
                >
                  {t('pages.credentials.userSidebar.unbind')}
                </button>
              </div>
              <p className="text-xs text-neutral-6">
                {t('pages.credentials.userSidebar.nodeBoundHint')}
              </p>
            </div>
          ) : (
            <>
              {entry.ou_ids.length > 0 ? (
                <div className="flex flex-wrap gap-1.5">
                  {entry.ou_ids.map((id) => (
                    <span
                      key={id}
                      className="flex items-center gap-1.5 rounded border border-neutral-11 px-2 py-0.5 text-xs text-neutral-3"
                    >
                      {ous.find((o) => o.id === id)?.name ?? id}
                      <button
                        type="button"
                        disabled={busy}
                        className="text-neutral-7 hover:text-status-error"
                        onClick={() =>
                          run(() =>
                            editAccessEntry(entry.id, (current) =>
                              withUnitRemoved(current, id),
                            ),
                          )
                        }
                      >
                        ×
                      </button>
                    </span>
                  ))}
                </div>
              ) : (
                <p className="text-xs text-neutral-6">
                  {entry.scope === 'account'
                    ? t('pages.credentials.accountWideOption')
                    : t('pages.credentials.unassigned')}
                </p>
              )}
              <p className="text-xs text-neutral-6">
                {t('pages.credentials.access.dragToAssignHint')}
              </p>
            </>
          )}
        </div>

        {/* Keys */}
        <div className="flex flex-col gap-2">
          <div className="text-sm font-medium text-neutral-7 uppercase">
            {t('pages.credentials.userSidebar.keys')}
          </div>
          {entry.key_ids.length === 0 ? (
            <p className="text-xs text-neutral-6">
              {t('pages.credentials.access.noKeysYet')}
            </p>
          ) : (
            entry.key_ids.map((id) => (
              <div
                key={id}
                className="flex items-center justify-between text-sm"
              >
                <span className="truncate text-neutral-3">{keyLabel(id)}</span>
                <button
                  type="button"
                  disabled={busy}
                  className="text-neutral-7 hover:text-status-error text-xs"
                  onClick={() =>
                    run(() => apiAccess.detachAccessKey(entry.id, id))
                  }
                >
                  {t('pages.credentials.access.detach')}
                </button>
              </div>
            ))
          )}
          <div className="flex items-center gap-2">
            <select
              value={attachId}
              disabled={busy || attachable.length === 0}
              onChange={(e) => setAttachId(e.target.value)}
              className="flex-1 rounded border border-neutral-11 bg-neutral-13 px-2 py-1.5 text-sm text-neutral-2"
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
            <Button
              size="sm"
              disabled={busy || !attachId}
              onClick={() =>
                run(async () => {
                  await apiAccess.attachAccessKey(entry.id, attachId)
                  setAttachId('')
                })
              }
            >
              {t('pages.credentials.access.attach')}
            </Button>
          </div>
        </div>

        {/* Principals: certificate principals accepted to log in AS this user.
            Only effective where a CA is trusted (CA tab / drag a CA onto a
            unit). Managed per-principal so a stale view can't wipe the set. */}
        <div className="flex flex-col gap-2">
          <div className="text-sm font-medium text-neutral-7 uppercase">
            {t('pages.credentials.userSidebar.principals')}
          </div>
          {(entry.principals ?? []).length === 0 ? (
            <p className="text-xs text-neutral-6">
              {t('pages.credentials.userSidebar.principalsNone')}
            </p>
          ) : (
            <div className="flex flex-wrap gap-1.5">
              {(entry.principals ?? []).map((p) => (
                <span
                  key={p}
                  className="flex items-center gap-1.5 rounded border border-neutral-11 px-2 py-0.5 text-xs text-neutral-3"
                >
                  {p}
                  <button
                    type="button"
                    disabled={busy}
                    className="text-neutral-7 hover:text-status-error"
                    onClick={() =>
                      run(() =>
                        editAccessEntry(entry.id, (current) =>
                          withPrincipalRemoved(current, p),
                        ),
                      )
                    }
                  >
                    ×
                  </button>
                </span>
              ))}
            </div>
          )}
          <div className="flex items-center gap-2">
            <Input
              type="text"
              placeholder={t('pages.credentials.userSidebar.principalsAdd')}
              value={pendingPrincipal}
              onChange={(e) => setPendingPrincipal(e.target.value)}
              disabled={busy}
            />
            <Button
              size="sm"
              disabled={busy || !pendingPrincipal.trim()}
              onClick={() =>
                run(async () => {
                  const principal = pendingPrincipal.trim()
                  await editAccessEntry(entry.id, (current) =>
                    withPrincipalAdded(current, principal),
                  )
                  setPendingPrincipal('')
                })
              }
            >
              {t('pages.credentials.access.attach')}
            </Button>
          </div>
        </div>

        {/* Sudo */}
        <label className="flex items-center gap-2 text-sm text-neutral-4">
          <input
            type="checkbox"
            checked={entry.sudo}
            disabled={busy}
            onChange={(e) =>
              run(() =>
                apiAccess.updateAccessEntry(entry.id, {
                  sudo: e.target.checked,
                }),
              )
            }
          />
          {t('pages.credentials.userSidebar.sudo')}
        </label>

        {/* Password */}
        <div className="flex flex-col gap-2">
          <div className="text-sm font-medium text-neutral-7 uppercase">
            {t('pages.credentials.userSidebar.password')}
          </div>
          <div className="flex items-center gap-2">
            <Input
              type="password"
              placeholder={
                entry.has_password
                  ? t('pages.credentials.access.passwordSet')
                  : t('pages.credentials.access.passwordNone')
              }
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={busy}
            />
            <Button
              size="sm"
              disabled={busy || !password}
              onClick={() =>
                run(async () => {
                  await apiAccess.updateAccessEntry(entry.id, { password })
                  setPassword('')
                })
              }
            >
              {t('pages.credentials.access.setPassword')}
            </Button>
          </div>
          {entry.has_password ? (
            <button
              type="button"
              disabled={busy}
              className="self-start text-neutral-7 hover:text-status-error text-xs"
              onClick={() =>
                run(() =>
                  apiAccess.updateAccessEntry(entry.id, {
                    clear_password: true,
                  }),
                )
              }
            >
              {t('pages.credentials.access.clearPassword')}
            </button>
          ) : null}
        </div>

        {error ? (
          <div className="text-status-error text-sm">{error}</div>
        ) : null}

        <Button
          variant="destructive"
          disabled={busy}
          onClick={() => setConfirmDelete(true)}
        >
          {t('pages.credentials.access.deleteUser')}
        </Button>
      </div>

      <ConfirmDialog
        open={confirmDelete}
        title={t('pages.credentials.userSidebar.deleteTitle')}
        message={t('pages.credentials.userSidebar.deleteBody', {
          username: entry.username,
        })}
        destructive
        busy={busy}
        confirmText={t('pages.credentials.access.deleteUser')}
        onCancel={() => setConfirmDelete(false)}
        onConfirm={() =>
          run(async () => {
            await apiAccess.deleteAccessEntry(entry.id)
            setConfirmDelete(false)
            onClose()
          })
        }
      />
    </div>
  )
}
