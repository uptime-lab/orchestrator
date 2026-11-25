import { describe, expect, it, vi } from 'vitest'

// The API client touches `window` when it loads; the outcome reader needs
// none of it.
vi.mock('@/shared/utils/apiConfig', () => ({ publicApiClient: {} }))

const { signUpOutcome } = await import('./postSignUp.ts')

const session = {
  access_token: 'a',
  refresh_token: 'r',
  expires_in: 900,
  token_type: 'Bearer',
  user: { id: 'u', email: 'a@example.com', name: 'A' },
}

describe('signUpOutcome', () => {
  // An open registration answers 202 with no session; the page must not
  // treat it as a sign-in.
  it('reads an open sign-up as "check your email"', () => {
    expect(signUpOutcome(202, { verification_pending: true })).toEqual({
      kind: 'check_email',
    })
  })

  it('keeps the session of every other sign-up', () => {
    expect(signUpOutcome(201, session as never)).toEqual({
      kind: 'session',
      result: session,
    })
  })
})
