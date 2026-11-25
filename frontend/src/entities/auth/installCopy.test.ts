import { describe, expect, it } from 'vitest'

// The words come from the locale file, so the bundle has to be initialised
// before the first t() call — otherwise every assertion reads a key back.
import '@/i18n'

import type { SignInOptions } from '@/entities/signInOptions'

import type { LinkedIdentity, OidcConfig } from './api/oidc.ts'
import {
  findLinkedIdentity,
  oidcAsksForInvite,
  oidcButtonText,
  oidcProviderName,
  passwordResetAffordance,
  signUpLinkShown,
  signUpOutlook,
} from './installCopy.ts'

const google: OidcConfig = {
  enabled: true,
  label: 'Sign in with Google',
  provider: 'Google',
  issuer: 'https://accounts.google.com',
  sign_up: 'invite_code',
}
const keycloak: OidcConfig = {
  enabled: true,
  label: 'Sign in with Keycloak',
  provider: 'Keycloak',
  issuer: 'https://id.example.com/realms/lab',
  sign_up: 'closed',
}
const unnamed: OidcConfig = {
  enabled: true,
  label: 'Sign in with SSO',
  provider: '',
  issuer: 'https://id.example.com/realms/lab',
  sign_up: 'closed',
}
// A backend that predates provider/issuer/sign_up.
const legacy: OidcConfig = { enabled: true, label: 'Sign in with Google' }

const mail = (
  email_delivery: boolean,
  registration?: SignInOptions['registration'],
): SignInOptions => ({
  email_delivery,
  registration,
})

describe('the identity provider is the install’s, not Google', () => {
  it('names Google on the hosted product, by default', () => {
    expect(oidcButtonText('sign-in', google)).toBe('Sign in with Google')
    expect(oidcButtonText('sign-up', google)).toBe('Sign up with Google')
  })

  it('names a configured provider and never says Google', () => {
    expect(oidcButtonText('sign-in', keycloak)).toBe('Sign in with Keycloak')
    expect(oidcButtonText('sign-up', keycloak)).toBe('Sign up with Keycloak')
  })

  it('names the identity provider generically when the install names none', () => {
    expect(oidcButtonText('sign-in', unnamed)).toBe(
      'Sign in with your identity provider',
    )
    expect(oidcButtonText('sign-up', unnamed)).toBe(
      'Sign up with your identity provider',
    )
    expect(oidcProviderName(unnamed, true)).toBe('Single sign-on')
    expect(oidcProviderName(null)).toBe('single sign-on')
  })

  it('keeps an older backend’s own label on the sign-in page', () => {
    expect(oidcButtonText('sign-in', legacy)).toBe('Sign in with Google')
  })

  it('asks for an invite code only where the callback would', () => {
    expect(oidcAsksForInvite(google)).toBe(true)
    expect(oidcAsksForInvite(keycloak)).toBe(false)
    expect(oidcAsksForInvite({ ...keycloak, sign_up: 'open' })).toBe(false)
    expect(oidcAsksForInvite(legacy)).toBe(true)
  })
})

describe('forgot password follows the mailer', () => {
  it('offers the reset link where mail is configured', () => {
    expect(passwordResetAffordance(mail(true)).kind).toBe('link')
  })

  it('points at the operator where mail is not configured', () => {
    const got = passwordResetAffordance(mail(false))
    expect(got.kind).toBe('operator')
    if (got.kind !== 'operator') return
    expect(got.text).toContain('operator of this install')
    expect(got.text).toContain('from the server')
    expect(got.text).toContain('--reset-password')
    expect(got.text).not.toContain('email')
  })

  it('keeps the link when the install could not be asked', () => {
    expect(passwordResetAffordance(null).kind).toBe('link')
  })
})

describe('the sign-up page follows the registration state', () => {
  it('admits the first owner without asking for a code', () => {
    const got = signUpOutlook(mail(false, 'first_owner'), null)
    expect(got.passwordForm).toBe(true)
    expect(got.inviteField).toBe(false)
    expect(got.note).toContain('first one')
  })

  // The provider's callback asks for the configured invite code even for the
  // first owner, so the note must not tell the whole page that no code is
  // needed while the panel below it asks for one.
  it('scopes the first-owner note to the email form where the provider asks for a code', () => {
    const got = signUpOutlook(mail(false, 'first_owner'), google)
    expect(got.oidc).toBe(true)
    expect(oidcAsksForInvite(google)).toBe(true)
    expect(got.note).toContain('with an email and password')
    expect(got.note).toContain('no invite code is needed')
  })

  it('keeps the plain first-owner note where the provider asks for no code', () => {
    const open = { ...keycloak, sign_up: 'open' as const }
    const got = signUpOutlook(mail(false, 'first_owner'), open)
    expect(got.oidc).toBe(true)
    expect(got.note).not.toContain('email and password')
  })

  it('asks for the code where the operator configured one', () => {
    const got = signUpOutlook(mail(true, 'invite_code'), google)
    expect(got).toMatchObject({
      passwordForm: true,
      inviteField: true,
      oidc: true,
      note: null,
    })
  })

  it('says sign-up is closed instead of showing a form the backend refuses', () => {
    const got = signUpOutlook(mail(false, 'closed'), keycloak)
    expect(got.passwordForm).toBe(false)
    expect(got.oidc).toBe(false)
    expect(got.note).toContain('closed')
    expect(signUpLinkShown(mail(false, 'closed'), keycloak)).toBe(false)
  })

  it('keeps the provider path where it still creates accounts', () => {
    const open = { ...keycloak, sign_up: 'open' as const }
    const got = signUpOutlook(mail(false, 'closed'), open)
    expect(got.passwordForm).toBe(false)
    expect(got.oidc).toBe(true)
    expect(got.note).toBeNull()
    expect(signUpLinkShown(mail(false, 'closed'), open)).toBe(true)
  })

  // Open registration: the form, no invite field, and the sign-in page
  // offers the sign-up link exactly as it does for an invite-code install.
  it('shows the form without a code field where registration is open', () => {
    const open = { ...keycloak, sign_up: 'open' as const }
    const got = signUpOutlook(mail(true, 'open'), open)
    expect(got).toEqual({
      passwordForm: true,
      inviteField: false,
      oidc: true,
      note: null,
    })
    expect(oidcAsksForInvite(open)).toBe(false)
    expect(signUpLinkShown(mail(true, 'open'), open)).toBe(true)
    expect(signUpLinkShown(mail(true, 'open'), null)).toBe(true)
    expect(signUpOutlook(mail(true, 'open'), null).oidc).toBe(false)
  })

  // Open registration on an install that cannot send the confirmation mail
  // is closed, and says why; an ordinary closed install does not.
  it('says sign-up is closed for want of mail where that is the reason', () => {
    const noMail: SignInOptions = {
      email_delivery: false,
      registration: 'closed',
      registration_reason: 'email_unavailable',
    }
    const got = signUpOutlook(noMail, keycloak)
    expect(got.passwordForm).toBe(false)
    expect(got.note).toContain('confirmation email')
    expect(signUpOutlook(mail(false, 'closed'), keycloak).note).not.toContain(
      'confirmation email',
    )
  })

  it('keeps the page as it was when the state is unknown', () => {
    expect(signUpOutlook(null, google)).toEqual({
      passwordForm: true,
      inviteField: true,
      oidc: true,
      note: null,
    })
    expect(signUpLinkShown(null, null)).toBe(true)
  })
})

describe('the linked identity is the install provider’s', () => {
  const ids: LinkedIdentity[] = [
    {
      id: 'a',
      provider: 'https://accounts.google.com',
      email: 'a@example.com',
      created_at: '',
    },
    {
      id: 'b',
      provider: 'https://id.example.com/realms/lab/',
      email: 'b@example.com',
      created_at: '',
    },
  ]

  // The account page once matched Google's issuer only, so an identity linked
  // through any other provider showed as "Not linked" with a Link button.
  it('finds a non-Google provider’s identity by issuer', () => {
    expect(findLinkedIdentity(ids, keycloak)?.id).toBe('b')
    expect(findLinkedIdentity(ids, google)?.id).toBe('a')
  })

  // go-oidc accepts a Google token whose iss carries no scheme, and the
  // stored provider is that iss verbatim.
  it('matches an issuer stored without the scheme', () => {
    const bare: LinkedIdentity[] = [
      {
        id: 'g',
        provider: 'accounts.google.com',
        email: 'g@example.com',
        created_at: '',
      },
    ]
    expect(findLinkedIdentity(bare, google)?.id).toBe('g')
    expect(
      findLinkedIdentity(ids, { issuer: 'accounts.google.com/' })?.id,
    ).toBe('a')
    expect(
      findLinkedIdentity(bare, { issuer: 'HTTPS://Accounts.Google.com' })?.id,
    ).toBe('g')
    expect(findLinkedIdentity(bare, keycloak)).toBeUndefined()
  })

  it('takes the only identity when an older backend sends no issuer', () => {
    expect(findLinkedIdentity(ids.slice(1), legacy)?.id).toBe('b')
  })
})
