import { t } from 'i18next'
import { useEffect, useState } from 'react'

import { authModel } from '@/entities/auth'
import {
  listSessions,
  revokeOtherSessions,
  revokeSession,
  type Session,
} from '@/entities/auth/api/sessions'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { describeUserAgent } from './userAgent'

// sessionLabel: "Chrome on macOS" for a recognised browser, the fallback for
// anything else. The raw User-Agent always travels in the tooltip.
const sessionLabel = (ua: string | undefined | null): string => {
  const described = describeUserAgent(ua)
  if (!described) return t('pages.account.sessions.unknownDevice')
  return described.os
    ? t('pages.account.sessions.browserOnOs', {
        browser: described.browser,
        os: described.os,
      })
    : described.browser
}

// SessionsSection shows the user every place they are signed in, and lets them
// end any of them.
//
// Without this list, sessions are invisible: live refresh tokens accumulate
// with no way to see or stop them. A session here is one LOGIN — the token
// behind it rotates constantly, but the session is the thing a person
// recognises as "my laptop".
export const SessionsSection = () => {
  const [items, setItems] = useState<Session[] | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  // Signing a session out ends that browser's access at once, so it asks
  // first; 'others' is the sign-out-everywhere-else button.
  const [confirming, setConfirming] = useState<Session | 'others' | null>(null)

  const refresh = () => {
    listSessions()
      .then(setItems)
      .catch(() => setItems([]))
  }

  useEffect(refresh, [])

  if (items === null) return null

  const run = async (action: () => Promise<void>) => {
    setError(null)
    setPending(true)
    try {
      await action()
      refresh()
    } catch (e: unknown) {
      setError(extractApiErrorMessage(e, t('pages.account.sessions.error')))
    } finally {
      setPending(false)
    }
  }

  const others = items.filter((s) => !s.current).length

  return (
    <div className="flex flex-col gap-3 border border-neutral-10 rounded p-4 mb-4">
      <div className="text-sm uppercase font-bold">
        {t('pages.account.sessions.title')}
      </div>
      <p className="text-sm text-neutral-7">
        {t('pages.account.sessions.description')}
      </p>

      <div className="flex flex-col gap-2">
        {items.map((session) => (
          <div
            key={session.id}
            className="flex items-center gap-4 border border-neutral-10 rounded px-3 py-2"
          >
            <div className="min-w-0 flex-1">
              {/* The label truncates and the marker does not, so a long
                  User-Agent can never push "this device" out of view. */}
              <div className="flex min-w-0 items-baseline gap-2 text-sm text-neutral-5">
                <span className="truncate" title={session.user_agent || ''}>
                  {sessionLabel(session.user_agent)}
                </span>
                {session.current && (
                  <span className="flex-shrink-0 text-xs text-lime-6">
                    {t('pages.account.sessions.current')}
                  </span>
                )}
              </div>
              <div className="text-xs text-neutral-7">
                {session.ip ? `${session.ip} · ` : ''}
                {t('pages.account.sessions.lastUsed', {
                  when: new Date(session.last_used_at).toLocaleString(),
                })}
              </div>
            </div>
            <Button
              size="sm"
              variant="destructive"
              disabled={pending}
              onClick={() => setConfirming(session)}
            >
              {t('pages.account.sessions.revoke')}
            </Button>
          </div>
        ))}
      </div>

      {others > 0 && (
        <div>
          <Button
            size="sm"
            variant="destructive"
            disabled={pending}
            onClick={() => setConfirming('others')}
          >
            {t('pages.account.sessions.revokeOthers', { count: others })}
          </Button>
        </div>
      )}

      {error && <div className="text-xs text-red-9">{error}</div>}

      <ConfirmDialog
        open={confirming !== null}
        title={
          confirming === 'others'
            ? t('pages.account.sessions.revokeOthersTitle')
            : t('pages.account.sessions.revokeTitle')
        }
        message={
          confirming === 'others'
            ? t('pages.account.sessions.revokeOthersMessage', {
                count: others,
              })
            : confirming?.current
              ? t('pages.account.sessions.revokeCurrentMessage')
              : t('pages.account.sessions.revokeMessage', {
                  device: confirming ? sessionLabel(confirming.user_agent) : '',
                })
        }
        confirmText={t('pages.account.sessions.revoke')}
        destructive
        busy={pending}
        onConfirm={() => {
          const target = confirming
          setConfirming(null)
          if (target === 'others') void run(revokeOtherSessions)
          else if (target?.current)
            // This browser's own session: end it here as well, rather than
            // leave the page signed in until its next renewal fails.
            void run(async () => {
              await revokeSession(target.id)
              authModel.events.signOut()
            })
          else if (target) void run(() => revokeSession(target.id))
        }}
        onCancel={() => setConfirming(null)}
      />
    </div>
  )
}
