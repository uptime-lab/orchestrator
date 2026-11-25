import { Dialog, DialogPanel, DialogTitle } from '@headlessui/react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { authHooks, authModel } from '@/entities/auth'
import { getTermsUrl, needsTermsAcceptance } from '@/entities/terms'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

// TermsGate asks a signed-in user to accept the operator's terms once, where
// the install set a terms link and the backend holds no acceptance for them.
//
// The sign-up form asks before the account exists, but it is not the only way
// in: an identity provider creates an account with no form at all, an
// invitation has its own page, and an account can be older than the terms.
// Asking here, on the layout every signed-in page shares, covers all of them
// with one question. The acceptance is stored by the backend, so it survives
// a new browser and leaves a record of who accepted and when.
export const TermsGate = () => {
  const { t } = useTranslation()
  const user = authHooks.useCurrentUser()
  const termsUrl = getTermsUrl()
  const [accepted, setAccepted] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!termsUrl || !needsTermsAcceptance(termsUrl, user)) {
    return null
  }

  const confirm = async () => {
    setError(null)
    setPending(true)
    try {
      // On success the auth store flips the flag and this component renders
      // nothing. A failure leaves it false: an acceptance that was not stored
      // is asked for again, never assumed.
      await authModel.acceptTermsFx()
    } catch (e: unknown) {
      setError(extractApiErrorMessage(e, t('entities.terms.gate.error')))
    } finally {
      setPending(false)
    }
  }

  return (
    // onClose does nothing: Esc, a click outside and a lost focus are not an
    // answer. The ways out are the box, or signing out, so that refusing is a
    // real choice and not a session that cannot be used.
    <Dialog open onClose={() => undefined} className="relative z-50">
      <div className="fixed inset-0 bg-neutral-15/90" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="flex w-full max-w-lg flex-col gap-4 rounded border border-neutral-11 bg-neutral-14 p-5 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase text-neutral-1">
            {t('entities.terms.gate.title')}
          </DialogTitle>
          <p className="text-sm text-neutral-4">
            {t('entities.terms.gate.intro')}
          </p>
          <label className="flex cursor-pointer select-none items-start gap-2">
            <input
              type="checkbox"
              checked={accepted}
              onChange={(e) => setAccepted(e.target.checked)}
              disabled={pending}
              className="mt-0.5 accent-lime-600"
            />
            <span className="text-sm text-neutral-2">
              {t('entities.terms.agree')}{' '}
              {/* A new tab: this dialog blocks the app until it is answered,
                  and navigating in place would tear it down mid-read. */}
              <a
                href={termsUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="text-lime-6 underline hover:text-lime-5"
              >
                {t('entities.terms.link')}
              </a>
            </span>
          </label>
          {error && <p className="text-sm text-red-5">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button
              size="md"
              kind="secondary"
              disabled={pending}
              onClick={() => authModel.events.signOut()}
            >
              {t('entities.terms.gate.decline')}
            </Button>
            <Button
              size="md"
              kind="primary"
              disabled={!accepted || pending}
              onClick={() => void confirm()}
            >
              {t('entities.terms.gate.continue')}
            </Button>
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}
