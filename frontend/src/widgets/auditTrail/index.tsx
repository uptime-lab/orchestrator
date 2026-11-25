import { FC, useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { apiAccount } from '@/entities/account/api'
import { currentAccountId } from '@/entities/account/model'
import { getAuditEvents, type AuditEvent } from '@/entities/auditEvent'
import { useUsers } from '@/entities/user/hooks'

import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { describeAuditEvent } from './describe'
import { describeAuditRetention } from './retention'

const PAGE_SIZE = 50

// The account's audit trail.
//
// This page answers "who deleted that node" or "who read this credential
// before it was rotated" without a database shell. Nothing in the API can delete a
// row; the retention sweep removes events older than the account's retention,
// which the account record states and this page repeats in one line.
//
// Read-only by construction: no filters that mutate, no actions, no selection.
export const AuditTrail: FC = () => {
  const { t } = useTranslation()
  const [events, setEvents] = useState<AuditEvent[] | null>(null)
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [actionFilter, setActionFilter] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [retentionDays, setRetentionDays] = useState<number | undefined>()

  // How long the trail is kept comes from the account record. A failed read
  // shows no line rather than a guess.
  useEffect(() => {
    const accountId = currentAccountId()
    if (!accountId) return
    apiAccount
      .getAccount(accountId)
      .then((acc) => setRetentionDays(acc.audit_retention_days))
      .catch(() => {})
  }, [])
  const retentionLine = describeAuditRetention(retentionDays)

  // Actor ids are uuids; the roster turns them into names. Failing to load it
  // degrades to the id rather than blanking the column — an id is still an
  // answer.
  const { users } = useUsers()
  const actorNames = useMemo(() => {
    const map: Record<string, string> = {}
    users.forEach((user) => {
      map[user.id] = user.name || user.email || user.id
    })
    return map
  }, [users])

  const load = useCallback(() => {
    setLoading(true)
    setError(null)
    getAuditEvents({
      page,
      per_page: PAGE_SIZE,
      action: actionFilter.trim() || undefined,
    })
      .then((result) => {
        setEvents(result.events)
        setTotal(result.total)
      })
      .catch((err) => {
        setEvents([])
        setError(extractApiErrorMessage(err, t('widgets.auditTrail.loadError')))
      })
      .finally(() => setLoading(false))
  }, [page, actionFilter, t])

  useEffect(load, [load])

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const rangeFrom = total === 0 ? 0 : (page - 1) * PAGE_SIZE + 1
  const rangeTo = Math.min(page * PAGE_SIZE, total)

  return (
    <div className="flex flex-col gap-4">
      <div className="text-sm text-neutral-4">
        {t('widgets.auditTrail.description')}
        {retentionLine && <> {retentionLine}</>}
      </div>

      <div className="flex items-center gap-2">
        <input
          type="text"
          value={actionFilter}
          onChange={(e) => {
            setActionFilter(e.target.value)
            setPage(1)
          }}
          placeholder={t('widgets.auditTrail.filterPlaceholder')}
          className="w-72 bg-neutral-14 border border-neutral-10 rounded px-2.5 py-2 text-sm text-white outline-none focus:border-lime-6"
        />
        {actionFilter && (
          <button
            type="button"
            onClick={() => {
              setActionFilter('')
              setPage(1)
            }}
            className="text-xs text-neutral-5 hover:text-neutral-2"
          >
            {t('widgets.auditTrail.clear')}
          </button>
        )}
      </div>

      {error && <div className="text-sm text-red-6">{error}</div>}

      {events === null || loading ? (
        <div className="text-sm text-neutral-5">
          {t('widgets.auditTrail.loading')}
        </div>
      ) : events.length === 0 ? (
        <div className="text-sm text-neutral-4 py-8 text-center">
          {actionFilter
            ? t('widgets.auditTrail.noMatches')
            : t('widgets.auditTrail.empty')}
        </div>
      ) : (
        <div className="flex flex-col gap-1">
          {events.map((event) => {
            const described = describeAuditEvent(event)
            return (
              <div
                key={event.id}
                className="flex items-baseline gap-3 border-b border-neutral-12 py-2 text-sm"
              >
                <span className="w-40 flex-shrink-0 font-mono text-xs text-neutral-6">
                  {new Date(event.created_at).toLocaleString()}
                </span>
                <span className="w-44 flex-shrink-0 truncate text-neutral-4">
                  {event.actor_user_id
                    ? actorNames[event.actor_user_id] || event.actor_user_id
                    : t('widgets.auditTrail.systemActor')}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="text-neutral-2">{described.summary}</span>
                  {described.detail && (
                    <span className="text-neutral-6">
                      {' '}
                      — {described.detail}
                    </span>
                  )}
                </span>
              </div>
            )
          })}
        </div>
      )}

      <div className="flex items-center justify-end gap-3 text-xs text-neutral-5">
        <span>
          {total === 0
            ? t('widgets.auditTrail.noEvents')
            : t('widgets.auditTrail.range', {
                from: rangeFrom,
                to: rangeTo,
                total,
              })}
        </span>
        <button
          type="button"
          onClick={() => setPage((p) => Math.max(1, p - 1))}
          disabled={page <= 1}
          className="px-2 py-1 text-neutral-4 hover:text-neutral-1 disabled:opacity-40 disabled:cursor-not-allowed"
          aria-label={t('widgets.auditTrail.previousPage')}
        >
          ‹
        </button>
        <button
          type="button"
          onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
          disabled={page >= totalPages}
          className="px-2 py-1 text-neutral-4 hover:text-neutral-1 disabled:opacity-40 disabled:cursor-not-allowed"
          aria-label={t('widgets.auditTrail.nextPage')}
        >
          ›
        </button>
      </div>
    </div>
  )
}
