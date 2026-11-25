import { Dialog, DialogPanel, DialogTitle } from '@headlessui/react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { accessChanged, apiAccess, type AccessEntry } from '@/entities/access'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

type Props = {
  open: boolean
  // Where the key was dropped: a unit, or the fleet-wide default bucket.
  ouId?: string
  accountWide?: boolean
  keyId: string
  keyLabel: string
  scopeLabel: string
  onClose: () => void
  // Called after a successful attach, with the scope it landed in, so the page
  // can navigate the rail there and make the result visible.
  onDone: () => void
}

/**
 * Prompted when an SSH key is dropped onto an OU (or the All-nodes bucket). A
 * key belongs to a named user, not to a unit — so instead of silently assigning
 * (the original bug), this asks which user in that scope should get the key, or
 * lets you create one. The attach is additive: it never replaces an existing
 * key set. On success it shows an explicit confirmation — an attach changes no
 * rail counter (the entry count is unchanged), so without this step the drop
 * looks like it did nothing.
 */
export const AttachKeyToUserDialog = ({
  open,
  ouId,
  accountWide,
  keyId,
  keyLabel,
  scopeLabel,
  onClose,
  onDone,
}: Props) => {
  const { t } = useTranslation()
  const [users, setUsers] = useState<AccessEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [newUsername, setNewUsername] = useState('')
  const [doneUser, setDoneUser] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setError(null)
    setNewUsername('')
    setDoneUser(null)
    setLoading(true)
    apiAccess
      .getAccessEntries(ouId ? { kind: 'ou', ouId } : { kind: 'account' })
      .then((entries) => setUsers(entries.filter((e) => e.type === 'user')))
      .catch(() => setError(t('pages.credentials.attach.loadError')))
      .finally(() => setLoading(false))
  }, [open, ouId, t])

  const attachTo = async (entryId: string, username: string) => {
    setBusy(true)
    setError(null)
    try {
      await apiAccess.attachAccessKey(entryId, keyId)
      // Notify the shared subscribers (users table, rail counts, sidebars) —
      // the attach mutated data they display.
      accessChanged()
      setDoneUser(username)
    } catch (e) {
      setError(
        extractApiErrorMessage(e, t('widgets.attachKeyToUser.attachError')),
      )
    } finally {
      setBusy(false)
    }
  }

  const createAndAttach = async () => {
    setBusy(true)
    setError(null)
    try {
      const username = newUsername.trim()
      await apiAccess.createAccessEntry({
        type: 'user',
        ou_ids: ouId ? [ouId] : undefined,
        account_wide: (!ouId && accountWide) || undefined,
        username,
        // Do not silently grant sudo from a drag: the dialog has no control for
        // it, and passwordless sudo is a privilege escalation. Grant it later in
        // the user's details sidebar.
        sudo: false,
        key_ids: [keyId],
      })
      accessChanged()
      setDoneUser(username)
    } catch (e) {
      setError(
        extractApiErrorMessage(e, t('widgets.attachKeyToUser.createError')),
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      open={open}
      onClose={doneUser ? onDone : onClose}
      className="relative z-50"
    >
      <div className="fixed inset-0 bg-neutral-15/80" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-md bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase text-neutral-1">
            {t('pages.credentials.attach.title')}
          </DialogTitle>

          {doneUser ? (
            <>
              <p className="text-sm text-lime-6">
                {t('pages.credentials.attach.success', {
                  key: keyLabel,
                  user: doneUser,
                  scope: scopeLabel,
                })}
              </p>
              <div className="flex justify-end">
                <Button size="sm" onClick={onDone}>
                  {t('pages.credentials.attach.done')}
                </Button>
              </div>
            </>
          ) : (
            <>
              <p className="text-sm text-neutral-5">
                {t('pages.credentials.attach.body', {
                  key: keyLabel,
                  scope: scopeLabel,
                })}
              </p>

              {loading ? (
                <div className="text-sm text-neutral-6">
                  {t('pages.credentials.attach.loading')}
                </div>
              ) : (
                <>
                  {users.length > 0 ? (
                    <div className="flex flex-col gap-1">
                      <div className="text-xs uppercase text-neutral-7">
                        {t('pages.credentials.attach.existingUsers')}
                      </div>
                      {users.map((u) => (
                        <button
                          key={u.id}
                          type="button"
                          disabled={busy}
                          onClick={() => attachTo(u.id, u.username ?? '')}
                          className="flex items-center justify-between rounded border border-neutral-12 px-3 py-2 text-left text-sm text-neutral-2 hover:border-lime-6 disabled:opacity-40"
                        >
                          <span>{u.username}</span>
                          <span className="text-neutral-7 text-xs">
                            {t('pages.credentials.attach.attachHere')}
                          </span>
                        </button>
                      ))}
                    </div>
                  ) : (
                    <div className="text-sm text-neutral-6">
                      {t('pages.credentials.attach.noUsers')}
                    </div>
                  )}

                  <div className="flex flex-col gap-2 border-t border-neutral-12 pt-3">
                    <div className="text-xs uppercase text-neutral-7">
                      {t('pages.credentials.attach.newUser')}
                    </div>
                    <div className="flex items-center gap-2">
                      <Input
                        type="text"
                        placeholder={t(
                          'pages.credentials.attach.usernamePlaceholder',
                        )}
                        value={newUsername}
                        onChange={(e) => setNewUsername(e.target.value)}
                        disabled={busy}
                      />
                      <Button
                        size="sm"
                        onClick={createAndAttach}
                        disabled={busy || !newUsername.trim()}
                      >
                        {t('pages.credentials.attach.createAndAttach')}
                      </Button>
                    </div>
                  </div>
                </>
              )}

              {error ? (
                <div className="text-status-error text-xs">{error}</div>
              ) : null}

              <div className="flex justify-end">
                <button
                  type="button"
                  onClick={onClose}
                  disabled={busy}
                  className="text-neutral-6 hover:text-neutral-2 text-sm disabled:opacity-40"
                >
                  {t('pages.credentials.attach.cancel')}
                </button>
              </div>
            </>
          )}
        </DialogPanel>
      </div>
    </Dialog>
  )
}
