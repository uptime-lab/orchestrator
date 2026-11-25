import { Dialog, DialogPanel, DialogTitle } from '@headlessui/react'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { startOidcStepUp } from '@/entities/auth/api/oidc'
import { isPasskeySupported } from '@/entities/auth/api/passkey'
import {
  confirmWithPasskey,
  confirmWithPassword,
  confirmWithTotp,
  getReauthMethods,
  type ReauthMethods,
} from '@/entities/auth/api/reauth'
import { subscribeToReauthPrompt } from '@/entities/auth/reauth/prompt'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input/input.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

type Resolver = (confirmed: boolean) => void

// ReauthDialogHost renders the confirmation prompt for the whole app.
//
// Mounted once, high in the tree: the request that triggers it can come from
// any screen — the axios interceptor suspends it, this asks, and the request
// resumes. Nothing renders until something actually asks.
export const ReauthDialogHost: FC = () => {
  const { t } = useTranslation()
  const [resolver, setResolver] = useState<Resolver | null>(null)
  const [methods, setMethods] = useState<ReauthMethods | null>(null)
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(
    () =>
      subscribeToReauthPrompt((resolve) => {
        setPassword('')
        setCode('')
        setError(null)
        setMethods(null)
        setResolver(() => resolve)
        // Which prompt to show depends on what the user actually holds; failing
        // to read it leaves the password field, which is the common case.
        getReauthMethods()
          .then(setMethods)
          .catch(() =>
            setMethods({
              password: true,
              totp: false,
              passkey: false,
              oidc: false,
              required: true,
            }),
          )
      }),
    [],
  )

  const finish = useCallback(
    (confirmed: boolean) => {
      resolver?.(confirmed)
      setResolver(null)
      setPassword('')
      setCode('')
      setError(null)
    },
    [resolver],
  )

  const attempt = useCallback(
    async (confirm: () => Promise<void>) => {
      setBusy(true)
      setError(null)
      try {
        await confirm()
        finish(true)
      } catch (err) {
        setError(
          extractApiErrorMessage(err, t('widgets.reauthDialog.mismatch')),
        )
      } finally {
        setBusy(false)
      }
    },
    [finish, t],
  )

  if (!resolver) {
    return null
  }

  const showPassword = methods?.password ?? true
  const showTotp = methods?.totp ?? false
  const showPasskey = (methods?.passkey ?? false) && isPasskeySupported()
  const showOidc = methods?.oidc ?? false

  return (
    <Dialog open onClose={() => finish(false)} className="relative z-50">
      <div className="fixed inset-0 bg-neutral-15/80" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-md bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase text-neutral-1">
            {t('widgets.reauthDialog.title')}
          </DialogTitle>
          <div className="text-sm text-neutral-4">
            {t('widgets.reauthDialog.description')}
          </div>

          {showPassword && (
            <form
              className="flex flex-col gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                if (password) void attempt(() => confirmWithPassword(password))
              }}
            >
              <Input
                label={t('widgets.reauthDialog.passwordLabel')}
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                disabled={busy}
              />
              <Button
                size="md"
                kind="primary"
                type="submit"
                disabled={busy || !password}
                grow
              >
                {t('widgets.reauthDialog.confirm')}
              </Button>
            </form>
          )}

          {showTotp && (
            <form
              className="flex flex-col gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                if (code) void attempt(() => confirmWithTotp(code))
              }}
            >
              <Input
                label={t('widgets.reauthDialog.totpLabel')}
                inputMode="numeric"
                autoComplete="one-time-code"
                value={code}
                onChange={(e) => setCode(e.target.value)}
                disabled={busy}
              />
              <Button
                size="md"
                kind={showPassword ? 'secondary' : 'primary'}
                type="submit"
                disabled={busy || !code}
                grow
              >
                {t('widgets.reauthDialog.confirmWithCode')}
              </Button>
            </form>
          )}

          {showPasskey && (
            <Button
              size="md"
              kind="secondary"
              onClick={() => void attempt(confirmWithPasskey)}
              disabled={busy}
              grow
            >
              {t('widgets.reauthDialog.usePasskey')}
            </Button>
          )}

          {showOidc && (
            <div className="flex flex-col gap-1">
              <Button
                size="md"
                kind={
                  showPassword || showTotp || showPasskey
                    ? 'secondary'
                    : 'primary'
                }
                onClick={() => {
                  // A redirect ends this page, so the request that asked for
                  // confirmation cannot be resumed. Release it as cancelled
                  // first, rather than leaving it hanging on a promise nothing
                  // will ever resolve.
                  finish(false)
                  void startOidcStepUp()
                }}
                disabled={busy}
                grow
              >
                {t('widgets.reauthDialog.confirmWithOidc')}
              </Button>
              <div className="text-xs text-neutral-7">
                {t('widgets.reauthDialog.oidcHint')}
              </div>
            </div>
          )}

          {error && <div className="text-sm text-red-6">{error}</div>}

          <div className="flex justify-end">
            <Button
              size="md"
              kind="secondary"
              onClick={() => finish(false)}
              disabled={busy}
            >
              {t('widgets.reauthDialog.cancel')}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}
