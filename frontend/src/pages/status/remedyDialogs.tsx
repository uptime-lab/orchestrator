import { Dialog, DialogPanel, DialogTitle } from '@headlessui/react'
import { useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { getCommandDetail } from '@/entities/fleet'
import type { RemedyOutcome } from '@/entities/fleet/remedies'

import { Button } from '@/shared/ui/button/button.tsx'

export type PendingRemedy = {
  remedyId: string
  /** Nodes this will actually reach: the condition holds and the caller may act. */
  eligible: { nodeId: string; nodeName: string }[]
  /** Selected nodes the caller may not act on. Named, never silently dropped. */
  deniedCount: number
  reboots: boolean
  /** Demands a ticked acknowledgement, not just a Continue. */
  acknowledge: boolean
}

// The confirmation. It states the count, and — where the remedy takes the
// device down — says so in its own sentence rather than behind a hover.
export const RemedyConfirmDialog = ({
  pending,
  busy,
  onConfirm,
  onCancel,
}: {
  pending: PendingRemedy | null
  busy: boolean
  onConfirm: () => void
  onCancel: () => void
}) => {
  const { t } = useTranslation()
  const [acknowledged, setAcknowledged] = useState(false)
  // Reset per dialog: a tick carried over from the last action would
  // acknowledge a consequence the operator has not been shown.
  useEffect(() => {
    setAcknowledged(false)
  }, [pending?.remedyId, pending?.eligible.length])

  if (!pending) {
    return null
  }
  const action = t(`pages.status.remedies.${pending.remedyId}.label` as never)
  const blocked = pending.acknowledge && !acknowledged
  return (
    <Dialog open onClose={busy ? () => {} : onCancel} className="relative z-50">
      <div className="fixed inset-0 bg-black/60" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-md bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase">
            {action}
          </DialogTitle>
          <div className="flex flex-col gap-2 text-sm text-neutral-4">
            <p>
              {t('pages.status.remedies.confirm.count', {
                count: pending.eligible.length,
              })}
            </p>
            <p>
              {t(`pages.status.remedies.${pending.remedyId}.effect` as never)}
            </p>
            {pending.reboots && (
              <p className="text-yellow-6">
                {t('pages.status.remedies.confirm.reboots')}
              </p>
            )}
            {pending.deniedCount > 0 && (
              <p className="text-neutral-6">
                {t('pages.status.remedies.confirm.denied', {
                  count: pending.deniedCount,
                })}
              </p>
            )}
          </div>
          {pending.acknowledge && (
            <label className="flex items-start gap-2 text-sm text-neutral-3">
              <input
                type="checkbox"
                checked={acknowledged}
                onChange={(e) => setAcknowledged(e.target.checked)}
                className="mt-0.5 accent-lime-6"
              />
              <span>
                {t(
                  `pages.status.remedies.${pending.remedyId}.acknowledge` as never,
                )}
              </span>
            </label>
          )}
          <div className="flex justify-end gap-2">
            <Button kind="tertiary" onClick={onCancel} disabled={busy}>
              {t('shared.confirmDialog.cancel')}
            </Button>
            <Button
              kind="primary"
              onClick={onConfirm}
              disabled={busy || blocked}
            >
              {busy
                ? t('pages.status.remedies.confirm.running')
                : t('pages.status.remedies.confirm.go')}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}

// The results. A toast is too small a surface: a skip carries the platform's
// reason and has to be readable, and "queued" has to be distinguishable from
// "done" — a queued command has been accepted, not applied.
export const RemedyResultsDialog = ({
  outcomes,
  onClose,
}: {
  outcomes: RemedyOutcome[] | null
  onClose: () => void
}) => {
  const { t } = useTranslation()
  if (!outcomes) {
    return null
  }
  return (
    <Dialog open onClose={onClose} className="relative z-50">
      <div className="fixed inset-0 bg-black/60" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-md bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase">
            {t('pages.status.remedies.results.title')}
          </DialogTitle>
          <div className="max-h-80 overflow-y-auto flex flex-col gap-1 text-sm">
            {outcomes.length === 0 && (
              <p className="text-neutral-6">
                {t('pages.status.remedies.results.empty')}
              </p>
            )}
            {outcomes.map((o) => (
              <div key={o.nodeId} className="flex justify-between gap-3">
                <span className="text-neutral-3 truncate">{o.nodeName}</span>
                <span
                  className={
                    o.state === 'failed'
                      ? 'text-red-5 text-right'
                      : o.state === 'skipped'
                        ? 'text-yellow-6 text-right'
                        : o.state === 'queued'
                          ? 'text-neutral-4 text-right'
                          : 'text-lime-6 text-right'
                  }
                >
                  {o.state === 'failed' && !o.reason
                    ? t('pages.status.remedies.results.failedNoReason')
                    : o.state === 'failed' || o.state === 'skipped'
                      ? t(`pages.status.remedies.results.${o.state}` as never, {
                          reason: o.reason,
                        })
                      : t(`pages.status.remedies.results.${o.state}` as never)}
                </span>
              </div>
            ))}
          </div>
          <div className="flex justify-end">
            <Button kind="secondary" onClick={onClose}>
              {t('pages.status.remedies.results.close')}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}

// Why one remedy did not work, in the agent's own words.
//
// Fetched per command rather than carried in the list: the details column is
// encrypted and gated on secret.read_value, so serving it for the whole fleet
// would decrypt every payload on a page load and — for a caller who holds that
// permission — stamp the audit trail with a read nobody performed. Opening one
// command is a read the operator actually made.
export const FailureDialog = ({
  commandId,
  onClose,
}: {
  commandId: string | null
  onClose: () => void
}) => {
  const { t } = useTranslation()
  const { data, isLoading, isError } = useQuery({
    queryKey: ['node-command', commandId],
    queryFn: () => getCommandDetail(commandId as string),
    enabled: !!commandId,
  })

  if (!commandId) {
    return null
  }
  return (
    <Dialog open onClose={onClose} className="relative z-50">
      <div className="fixed inset-0 bg-black/60" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-lg bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase">
            {t('pages.status.remedies.failure.title')}
          </DialogTitle>
          {isLoading && (
            <p className="text-sm text-neutral-6">
              {t('pages.status.loading')}
            </p>
          )}
          {isError && (
            <p className="text-sm text-red-5">
              {t('pages.status.remedies.failure.unreadable')}
            </p>
          )}
          {data && (
            <div className="flex flex-col gap-2 text-sm">
              <div className="flex justify-between gap-3 text-xs text-neutral-6">
                <span className="font-mono">{data.slug}</span>
                <span>{new Date(data.updated_at).toLocaleString()}</span>
              </div>
              {/* The agent's text verbatim, wrapped and scrollable: it carries
                  paths and device names an operator needs to read exactly. */}
              <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-neutral-15 p-3 font-mono text-xs text-neutral-3">
                {data.details || t('pages.status.remedies.failure.noMessage')}
              </pre>
              {/* details is often only a summary. The tool's own words live in
                  result, and they are what an operator can act on — the
                  Tailscale apply says "failed utilities: tailscale" in details
                  and "To approve your machine, visit …" here. */}
              {Object.entries(data.result ?? {})
                .filter(([key]) => key !== 'union_hash')
                .map(([key, value]) => (
                  <div key={key}>
                    <div className="text-xs text-neutral-6 font-mono">
                      {key}
                    </div>
                    <pre className="mt-0.5 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-neutral-15 p-3 font-mono text-xs text-neutral-3">
                      {value}
                    </pre>
                  </div>
                ))}
            </div>
          )}
          <div className="flex justify-end">
            <Button kind="secondary" onClick={onClose}>
              {t('pages.status.remedies.results.close')}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}
