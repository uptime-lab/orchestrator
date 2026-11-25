// parseTermsUrl accepts an absolute https URL and nothing else. Anything else —
// empty, relative, http, javascript:, a typo — counts as unset: a checkbox that
// links nowhere, or somewhere unsafe, is worse than no checkbox.
export const parseTermsUrl = (raw: unknown): string | null => {
  if (typeof raw !== 'string') {
    return null
  }
  const value = raw.trim()
  if (value === '') {
    return null
  }
  let url: URL
  try {
    url = new URL(value)
  } catch {
    return null
  }
  if (url.protocol !== 'https:' || url.hostname === '') {
    return null
  }
  return url.toString()
}

// needsTermsAcceptance says whether a signed-in user must still be asked. The
// sign-up form asks before the account exists; every other way in (an
// identity provider, an invitation, an account older than the terms) reaches
// the app with nothing stored, and is asked once there.
export const needsTermsAcceptance = (
  termsUrl: string | null,
  user: { terms_accepted: boolean } | undefined,
): boolean => !!termsUrl && !!user && !user.terms_accepted

// termsAcceptance decides what the sign-up request says about the terms. With
// no terms configured it says nothing; with terms configured the box must be
// ticked, and the request carries the acceptance so the backend records it.
export const termsAcceptance = (
  termsUrl: string | null,
  checked: boolean,
): { ok: boolean; terms_accepted?: true } => {
  if (!termsUrl) {
    return { ok: true }
  }
  return checked ? { ok: true, terms_accepted: true } : { ok: false }
}
