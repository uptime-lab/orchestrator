import { t } from 'i18next'

import type { SignInOptions } from '@/entities/signInOptions'

import type { LinkedIdentity, OidcConfig } from './api/oidc.ts'

// The sign-in, sign-up and account pages serve both the hosted product and
// self-hosted installs. These helpers decide their wording from what the
// install reports (GET /auth/oidc/config, GET /api/v1/server/sign-in-options)
// instead of assuming the hosted one: Google as the identity provider, a
// mailer behind "forgot password", an invite code for every sign-up.
//
// Every helper treats a missing signal (an older backend, a failed read) as
// "keep the page as it was", so the hosted product never loses a working
// affordance because a request failed.

// oidcProviderName is the provider's display name, or the generic words
// "single sign-on" when the install names none. The capitalised form is for
// a row label standing on its own.
export const oidcProviderName = (
  config: Pick<OidcConfig, 'provider'> | null | undefined,
  capitalised = false,
): string => {
  const name = config?.provider?.trim()
  if (name) return name
  return capitalised
    ? t('entities.auth.oidc.genericProviderTitle')
    : t('entities.auth.oidc.genericProvider')
}

// oidcButtonText is the federated button on the sign-in or sign-up page.
// An older backend sends no provider field at all; its own label is then
// the best wording available on the sign-in page.
export const oidcButtonText = (
  mode: 'sign-in' | 'sign-up',
  config: OidcConfig,
): string => {
  if (mode === 'sign-in' && config.provider === undefined) return config.label
  // An install that names no provider: "Sign in with single sign-on" reads
  // badly, so the button names the operator's identity provider instead.
  if (!config.provider?.trim()) {
    return mode === 'sign-in'
      ? t('entities.auth.signIn.oidcButtonGeneric')
      : t('entities.auth.signUp.oidcButtonGeneric')
  }
  const provider = oidcProviderName(config)
  return mode === 'sign-in'
    ? t('entities.auth.signIn.oidcButton', { provider })
    : t('entities.auth.signUp.oidcButtonProvider', { provider })
}

// oidcAsksForInvite: the invite field next to the federated button is
// shown only where the callback would ask for a code. Unknown (an older
// backend) keeps the field, as before.
export const oidcAsksForInvite = (config: OidcConfig): boolean =>
  config.sign_up === undefined || config.sign_up === 'invite_code'

// passwordResetAffordance: a reset link is offered only where mail can
// carry it. Without mail the page names the operator's server-side reset
// instead of a form whose request the backend refuses.
export type PasswordResetAffordance =
  | { kind: 'link' }
  | { kind: 'operator'; text: string }

export const passwordResetAffordance = (
  options: SignInOptions | null,
): PasswordResetAffordance =>
  options?.email_delivery === false
    ? {
        kind: 'operator',
        text: t('entities.auth.signIn.form.forgotPasswordOperator'),
      }
    : { kind: 'link' }

// SignUpOutlook is what the sign-up page shows.
export type SignUpOutlook = {
  // The email/password form.
  passwordForm: boolean
  // The invite code field inside that form.
  inviteField: boolean
  // The federated button (when OIDC is configured at all).
  oidc: boolean
  // A line above the form, when the install has something to say.
  note: string | null
}

// signUpOutlook follows the backend's admission order: an empty install
// admits its first owner without a code; with accounts, an install with open
// registration admits anyone without one, otherwise a sign-up needs the
// operator's invite code, or is closed. A closed password registration can
// still leave the federated path open (OIDC_AUTO_PROVISION).
export const signUpOutlook = (
  options: SignInOptions | null,
  oidc: OidcConfig | null,
): SignUpOutlook => {
  const oidcCanCreate = !!oidc && oidc.sign_up !== 'closed'
  switch (options?.registration) {
    case 'first_owner':
      // Only the email form skips the code. The provider's callback still
      // asks for the operator's invite code whenever one is configured, so
      // where its panel shows with the code field the note names the email
      // path rather than contradicting the panel below it.
      return {
        passwordForm: true,
        inviteField: false,
        oidc: oidcCanCreate,
        note:
          oidcCanCreate && oidcAsksForInvite(oidc)
            ? t('entities.auth.signUp.firstOwnerEmail')
            : t('entities.auth.signUp.firstOwner'),
      }
    case 'closed':
      return {
        passwordForm: false,
        inviteField: false,
        oidc: !!oidc && oidc.sign_up === 'open',
        note:
          !!oidc && oidc.sign_up === 'open'
            ? null
            : options.registration_reason === 'email_unavailable'
              ? t('entities.auth.signUp.closedNoMail')
              : t('entities.auth.signUp.closed'),
      }
    case 'open':
      // No code is asked for anywhere: the backend's provider callback
      // follows the same open gate, and says so with sign_up 'open'.
      return {
        passwordForm: true,
        inviteField: false,
        oidc: oidcCanCreate,
        note: null,
      }
    case 'invite_code':
      return {
        passwordForm: true,
        inviteField: true,
        oidc: oidcCanCreate,
        note: null,
      }
    default:
      // Unknown: the page as it always was.
      return { passwordForm: true, inviteField: true, oidc: !!oidc, note: null }
  }
}

// signUpLinkShown: the sign-in page's "create an account" footer is hidden
// only when the install is known to admit no sign-up at all.
export const signUpLinkShown = (
  options: SignInOptions | null,
  oidc: OidcConfig | null,
): boolean => {
  const outlook = signUpOutlook(options, oidc)
  return outlook.passwordForm || outlook.oidc
}

// findLinkedIdentity picks the identity linked through THIS install's
// provider. An older backend sends no issuer; then any linked identity is
// taken, since an install has one provider.
export const findLinkedIdentity = (
  identities: LinkedIdentity[],
  config: Pick<OidcConfig, 'issuer'> | null,
): LinkedIdentity | undefined => {
  const issuer = config?.issuer ? normaliseIssuer(config.issuer) : ''
  if (!issuer) return identities[0]
  return identities.find((i) => normaliseIssuer(i.provider) === issuer)
}

// normaliseIssuer makes two spellings of one issuer compare equal. The
// stored provider is the token's own `iss`, and Google's tokens may carry
// "accounts.google.com" without the scheme while the install is configured
// with "https://accounts.google.com". The scheme and trailing slashes go,
// and the host is lowercased; the path keeps its case.
export const normaliseIssuer = (issuer: string): string => {
  const bare = issuer
    .trim()
    .replace(/^[a-z][a-z0-9+.-]*:\/\//i, '')
    .replace(/\/+$/, '')
  const slash = bare.indexOf('/')
  return slash < 0
    ? bare.toLowerCase()
    : bare.slice(0, slash).toLowerCase() + bare.slice(slash)
}
