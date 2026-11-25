import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { accountLimitCard } from '@/entities/entitlement/accountLimit'
import {
  applyEntitlement,
  canApplyEntitlement,
  getEntitlement,
  isAccountLimitEntitlement,
  type AccountLimitEntitlement,
  type Entitlement,
  type EntitlementAnswer,
} from '@/entities/entitlement/api'
import { applyOutcome } from '@/entities/entitlement/applyOutcome'
import {
  deviceCountLines,
  type DeviceCount,
} from '@/entities/entitlement/deviceCounts'
import { freeFloorClamp } from '@/entities/entitlement/freeFloor'
import { stateLine } from '@/entities/entitlement/stateLine'
import {
  CONTACT_MARK,
  ContactText,
} from '@/entities/entitlement/ui/ContactText'

import { Button } from '@/shared/ui/button/button.tsx'
import { showSuccess } from '@/shared/ui/toast'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

// SubscriptionCard is the entitlement: what the install runs under, how many
// devices it holds against that (the operator) or how many the caller's own
// account holds (everyone), and, for the operator, the link to extend it and
// the paste that applies the next file. Nothing on it locks anything — the one effect
// of a lapse is on the next admission, and the card says exactly that.
//
// An account bound by the install's per-account device limit sees its own
// plan instead (AccountLimitSummary): its limit, its count, a dated
// subscription's end and whom to write to — never the install's
// entitlement, and never the paste, which is the operator's.
export const SubscriptionCard = () => {
  const { t } = useTranslation()
  const [entitlement, setEntitlement] = useState<EntitlementAnswer | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [document, setDocument] = useState('')
  const [applying, setApplying] = useState(false)
  const [applyError, setApplyError] = useState<string | null>(null)
  // A paste that was stored but is not in force: kept on the card, not a toast.
  const [applyNotice, setApplyNotice] = useState<string | null>(null)

  const load = () => {
    getEntitlement()
      .then((e) => {
        setEntitlement(e)
        setLoadError(null)
      })
      .catch((e: unknown) =>
        setLoadError(
          extractApiErrorMessage(e, t('widgets.subscriptionCard.loadFailed')),
        ),
      )
  }

  useEffect(load, [t])

  const handleApply = async () => {
    setApplyError(null)
    setApplyNotice(null)
    setApplying(true)
    try {
      const next = await applyEntitlement(document.trim())
      setEntitlement(next)
      setDocument('')
      const outcome = applyOutcome(next, formatDate)
      if (outcome.key === 'applied') {
        showSuccess(t('widgets.subscriptionCard.applied'))
      } else {
        const expires = outcome.values.expires ?? ''
        setApplyNotice(
          outcome.key === 'appliedLapsed'
            ? t('widgets.subscriptionCard.appliedLapsed', { expires })
            : t('widgets.subscriptionCard.appliedNotInForce', { expires }),
        )
      }
    } catch (e: unknown) {
      setApplyError(
        extractApiErrorMessage(e, t('widgets.subscriptionCard.applyFailed')),
      )
    } finally {
      setApplying(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-3 border border-neutral-10 rounded p-4">
        <div className="text-sm uppercase font-bold">
          {t('widgets.subscriptionCard.title')}
        </div>
        {loadError && <div className="text-xs text-red-9">{loadError}</div>}
        {entitlement &&
          (isAccountLimitEntitlement(entitlement) ? (
            <AccountLimitSummary entitlement={entitlement} />
          ) : (
            <EntitlementSummary entitlement={entitlement} />
          ))}
      </div>

      {canApplyEntitlement(entitlement) && (
        <ApplyForm
          document={document}
          setDocument={setDocument}
          applying={applying}
          applyError={applyError}
          applyNotice={applyNotice}
          onApply={handleApply}
        />
      )}
    </div>
  )
}

type ApplyFormProps = {
  document: string
  setDocument: (value: string) => void
  applying: boolean
  applyError: string | null
  applyNotice: string | null
  onApply: () => void
}

// ApplyForm is the paste, shown only to the account that operates the
// install: the server refuses it from anyone else.
const ApplyForm = ({
  document,
  setDocument,
  applying,
  applyError,
  applyNotice,
  onApply,
}: ApplyFormProps) => {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col gap-3 border border-neutral-10 rounded p-4">
      <div className="text-sm uppercase font-bold">
        {t('widgets.subscriptionCard.applyTitle')}
      </div>
      <p className="text-sm text-neutral-7">
        {t('widgets.subscriptionCard.applyHint')}
      </p>
      <textarea
        name="entitlement_document"
        value={document}
        onChange={(e) => setDocument(e.target.value)}
        placeholder={t('widgets.subscriptionCard.applyPlaceholder')}
        className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5 font-mono min-h-[120px] resize-y"
        disabled={applying}
        spellCheck={false}
      />
      {applyError && <div className="text-xs text-red-9">{applyError}</div>}
      {applyNotice && (
        <div className="bg-amber-6/10 border border-amber-6/50 text-amber-4 px-3 py-2 rounded text-xs">
          {applyNotice}
        </div>
      )}
      <div>
        <Button
          size="sm"
          disabled={applying || document.trim() === ''}
          onClick={onApply}
        >
          {applying
            ? t('widgets.subscriptionCard.applying')
            : t('widgets.subscriptionCard.apply')}
        </Button>
      </div>
    </div>
  )
}

const formatDate = (iso: string | undefined): string =>
  iso ? new Date(iso).toLocaleDateString() : ''

export const AccountLimitSummary = ({
  entitlement,
}: {
  entitlement: AccountLimitEntitlement
}) => {
  const { t } = useTranslation()
  const card = accountLimitCard(
    entitlement,
    formatDate,
    t('widgets.subscriptionCard.countUnknown'),
  )
  const line = (l: NonNullable<typeof card.remedy>) =>
    t(`widgets.subscriptionCard.account.${l.key}`, l.values)
  return (
    <div className="flex flex-col gap-2 text-sm">
      <div className="text-neutral-3" data-testid="account-plan">
        {line(card.plan)}
      </div>
      {card.remedy && (
        <div className="text-neutral-5" data-testid="account-remedy">
          {card.namesContact ? (
            <ContactText
              sentence={t(
                `widgets.subscriptionCard.account.${card.remedy.key}`,
                {
                  ...card.remedy.values,
                  contact: CONTACT_MARK,
                },
              )}
              contact={card.contact}
              className="underline text-purple-4"
            />
          ) : (
            line(card.remedy)
          )}
        </div>
      )}
      {card.ends && (
        <div className="text-xs text-neutral-7">{line(card.ends)}</div>
      )}
    </div>
  )
}

const EntitlementSummary = ({ entitlement }: { entitlement: Entitlement }) => {
  const { t } = useTranslation()
  const e = entitlement
  const unlimited = e.effective_device_cap === 0
  const capText = unlimited
    ? t('widgets.subscriptionCard.unlimited')
    : String(e.effective_device_cap)
  const counts = deviceCountLines(e)
  const countText = (n: DeviceCount): string =>
    n === 'unknown' ? t('widgets.subscriptionCard.countUnknown') : String(n)
  const clamp = freeFloorClamp(e)

  const state = stateLine(e, formatDate, capText)
  const stateText = t(`widgets.subscriptionCard.${state.key}`, state.values)

  const tone =
    e.state === 'lapsed' || e.state === 'rejected'
      ? 'text-amber-4'
      : e.state === 'grace'
        ? 'text-amber-3'
        : 'text-neutral-3'

  return (
    <div className="flex flex-col gap-2 text-sm">
      <div className={tone}>{stateText}</div>
      {counts.install !== null && (
        <div className="text-neutral-5">
          {t('widgets.subscriptionCard.devicesLine', {
            admitted: countText(counts.install),
            cap: capText,
          })}
        </div>
      )}
      {counts.account !== null && (
        <div className="text-neutral-5">
          {e.instance_operator
            ? t('widgets.subscriptionCard.accountDevicesLine', {
                devices: countText(counts.account),
              })
            : t('widgets.subscriptionCard.tenantDevicesLine', {
                devices: countText(counts.account),
              })}
        </div>
      )}
      {clamp && (
        <div className="text-xs text-amber-3">
          {t('widgets.subscriptionCard.belowFreeFloor', {
            signed: clamp.signedCap,
            free: clamp.freeCap,
          })}
        </div>
      )}
      {e.customer_id && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-xs text-neutral-6">
          <dt>{t('widgets.subscriptionCard.customer')}</dt>
          <dd className="text-neutral-4">{e.customer_id}</dd>
          <dt>{t('widgets.subscriptionCard.kind')}</dt>
          <dd className="text-neutral-4">
            {t(`widgets.subscriptionCard.kinds.${e.kind ?? 'subscription'}`)}
          </dd>
          <dt>{t('widgets.subscriptionCard.issued')}</dt>
          <dd className="text-neutral-4">{formatDate(e.issued_at)}</dd>
          <dt>{t('widgets.subscriptionCard.expires')}</dt>
          <dd className="text-neutral-4">{formatDate(e.expires_at)}</dd>
          <dt>{t('widgets.subscriptionCard.grace')}</dt>
          <dd className="text-neutral-4">
            {t('widgets.subscriptionCard.graceDays', {
              count: e.grace_days ?? 0,
            })}
          </dd>
          {e.note && (
            <>
              <dt>{t('widgets.subscriptionCard.note')}</dt>
              <dd className="text-neutral-4">{e.note}</dd>
            </>
          )}
          <dt>{t('widgets.subscriptionCard.source')}</dt>
          <dd className="text-neutral-4">
            {t(`widgets.subscriptionCard.sources.${e.source ?? 'none'}`)}
          </dd>
        </dl>
      )}
      <div className="text-xs text-neutral-7">
        {t('widgets.subscriptionCard.lapseExplainer')}
      </div>
      {e.rejections && e.rejections.length > 0 && (
        <div className="bg-amber-6/10 border border-amber-6/50 text-amber-4 px-3 py-2 rounded text-xs flex flex-col gap-1">
          <div className="font-bold">
            {t('widgets.subscriptionCard.rejectionsTitle')}
          </div>
          {e.rejections.map((r) => (
            <div key={`${r.source}:${r.reason}`}>
              {t(`widgets.subscriptionCard.sources.${r.source}`)}: {r.reason}
            </div>
          ))}
        </div>
      )}
      {counts.subscriptionLink && (
        <a
          href={e.subscription_url}
          target="_blank"
          rel="noreferrer"
          className="text-xs underline text-purple-4"
        >
          {t('widgets.subscriptionCard.subscriptionLink')}
        </a>
      )}
    </div>
  )
}
