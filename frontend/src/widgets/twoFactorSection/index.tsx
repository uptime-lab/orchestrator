import { t } from 'i18next'
import { useEffect, useState } from 'react'

import { apiAuth } from '@/entities/auth/api'
import type { TwoFactorEnrollment } from '@/entities/auth/api/twoFactor'
import { signInErrorMessage } from '@/entities/auth/signInError.ts'

import { Button } from '@/shared/ui/button/button.tsx'
import { Input } from '@/shared/ui/input'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

type Stage = 'idle' | 'enrolling' | 'recovery'

type TwoFactorSectionProps = {
  // required renders the "this account demands it" framing, used by the
  // forced-enrollment path.
  required?: boolean
  onEnabled?: () => void
}

// TwoFactorSection manages the signed-in user's second factor.
//
// The enrollment is deliberately two-step and the QR arrives already rendered
// from the server, so the shared secret never passes through a client-side QR
// library and a half-finished enrollment leaves the account untouched.
export const TwoFactorSection = ({
  required,
  onEnabled,
}: TwoFactorSectionProps) => {
  const [enabled, setEnabled] = useState<boolean | null>(null)
  const [remaining, setRemaining] = useState(0)
  const [stage, setStage] = useState<Stage>('idle')
  const [enrollment, setEnrollment] = useState<TwoFactorEnrollment | null>(null)
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([])
  const [code, setCode] = useState('')
  const [disabling, setDisabling] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)

  const refresh = () => {
    apiAuth
      .getTwoFactorStatus()
      .then((s) => {
        setEnabled(s.enabled)
        setRemaining(s.recovery_codes_remaining)
      })
      .catch(() => setEnabled(false))
  }

  useEffect(refresh, [])

  const startEnrollment = async () => {
    setError(null)
    setPending(true)
    try {
      setEnrollment(await apiAuth.postEnrollTwoFactor())
      setStage('enrolling')
    } catch (e: unknown) {
      setError(extractApiErrorMessage(e, t('pages.account.twoFactor.title')))
    } finally {
      setPending(false)
    }
  }

  const confirmEnrollment = async () => {
    setError(null)
    setPending(true)
    try {
      setRecoveryCodes(await apiAuth.postConfirmTwoFactor(code))
      setStage('recovery')
      setCode('')
      refresh()
      onEnabled?.()
    } catch (e: unknown) {
      setError(extractApiErrorMessage(e, t('pages.account.twoFactor.title')))
    } finally {
      setPending(false)
    }
  }

  const disable = async () => {
    setError(null)
    setPending(true)
    try {
      await apiAuth.deleteTwoFactor(code)
      setCode('')
      setDisabling(false)
      refresh()
    } catch (e: unknown) {
      setError(signInErrorMessage(e, t('pages.account.twoFactor.title')))
    } finally {
      setPending(false)
    }
  }

  if (enabled === null) return null

  return (
    <div className="flex flex-col gap-3 border border-neutral-10 rounded p-4 mb-4">
      <div className="text-sm uppercase font-bold">
        {t('pages.account.twoFactor.title')}
      </div>
      <p className="text-sm text-neutral-7">
        {required
          ? t('pages.account.twoFactor.required')
          : t('pages.account.twoFactor.description')}
      </p>

      {stage === 'recovery' && (
        <div className="flex flex-col gap-3 bg-neutral-11 rounded p-3">
          <div className="text-sm font-bold">
            {t('pages.account.twoFactor.recoveryTitle')}
          </div>
          <div className="text-xs text-neutral-7">
            {t('pages.account.twoFactor.recoveryHint')}
          </div>
          <ul className="grid grid-cols-2 gap-1">
            {recoveryCodes.map((rc) => (
              <li key={rc} className="text-sm text-lime-6 font-mono">
                {rc}
              </li>
            ))}
          </ul>
          <Button size="sm" onClick={() => setStage('idle')}>
            {t('pages.account.twoFactor.recoveryDone')}
          </Button>
        </div>
      )}

      {stage === 'enrolling' && enrollment && (
        <div className="flex flex-col gap-3">
          <div className="text-sm text-neutral-7">
            {t('pages.account.twoFactor.scan')}
          </div>
          {enrollment.qr_code && (
            <img
              src={enrollment.qr_code}
              alt=""
              width={240}
              height={240}
              className="bg-white p-2 rounded self-start"
            />
          )}
          <div className="text-xs text-neutral-7">
            {t('pages.account.twoFactor.manualEntry')}{' '}
            <code className="break-all text-lime-6">{enrollment.secret}</code>
          </div>
          <Input
            id="totp-code"
            name="code"
            type="text"
            label={t('pages.account.twoFactor.code')}
            value={code}
            onChange={(e) => setCode(e.target.value)}
            autoComplete="one-time-code"
          />
          <div className="flex gap-2">
            <Button size="sm" disabled={pending} onClick={confirmEnrollment}>
              {t('pages.account.twoFactor.confirm')}
            </Button>
            <Button
              size="sm"
              kind="secondary"
              onClick={() => {
                setStage('idle')
                setCode('')
                setError(null)
              }}
            >
              {t('pages.account.twoFactor.cancel')}
            </Button>
          </div>
        </div>
      )}

      {stage === 'idle' && (
        <div className="flex flex-col gap-3">
          <div className="text-sm text-neutral-5">
            {enabled
              ? t('pages.account.twoFactor.statusOn')
              : t('pages.account.twoFactor.statusOff')}
          </div>
          {enabled && remaining > 0 && (
            <div className="text-xs text-neutral-7">
              {t('pages.account.twoFactor.remaining', { count: remaining })}
            </div>
          )}
          {!enabled && (
            <Button size="sm" disabled={pending} onClick={startEnrollment}>
              {t('pages.account.twoFactor.enable')}
            </Button>
          )}
          {enabled && !disabling && (
            <Button
              size="sm"
              variant="destructive"
              onClick={() => setDisabling(true)}
            >
              {t('pages.account.twoFactor.disable')}
            </Button>
          )}
          {enabled && disabling && (
            <div className="flex flex-col gap-2">
              <div className="text-xs text-neutral-7">
                {t('pages.account.twoFactor.disablePrompt')}
              </div>
              <Input
                id="disable-code"
                name="code"
                type="text"
                label={t('pages.account.twoFactor.code')}
                value={code}
                onChange={(e) => setCode(e.target.value)}
                autoComplete="one-time-code"
              />
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={pending}
                  onClick={disable}
                >
                  {t('pages.account.twoFactor.disable')}
                </Button>
                <Button
                  size="sm"
                  kind="secondary"
                  onClick={() => {
                    setDisabling(false)
                    setCode('')
                    setError(null)
                  }}
                >
                  {t('pages.account.twoFactor.cancel')}
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {error && <div className="text-sm text-red-9">{error}</div>}
    </div>
  )
}
