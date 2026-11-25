import { useTranslation } from 'react-i18next'

import { getTermsUrl } from '../termsUrl'

// TERMS_FIELD_NAME is the form field the sign-up form reads the box from.
export const TERMS_FIELD_NAME = 'terms'

// The sign-up form's agreement box. Renders nothing unless the operator set a
// terms link, so a plain self-hosted install signs up with no agreement at all.
export const TermsAgreement = () => {
  const { t } = useTranslation()
  const termsUrl = getTermsUrl()

  if (!termsUrl) {
    return null
  }

  return (
    <label className="flex cursor-pointer select-none items-start gap-2">
      <input
        type="checkbox"
        name={TERMS_FIELD_NAME}
        required
        className="mt-0.5 accent-lime-600"
      />
      <span className="text-sm text-neutral-4">
        {t('entities.terms.agree')}{' '}
        {/* A new tab: navigating in place would throw away a half-filled form. */}
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
  )
}
