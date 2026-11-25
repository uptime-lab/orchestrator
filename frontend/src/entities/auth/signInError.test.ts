import { describe, expect, it } from 'vitest'

import '@/i18n'

import { signInErrorMessage } from './signInError'

const refusal = (
  status: number,
  code: string,
  headers: Record<string, unknown> = {},
) => ({
  response: {
    status,
    headers,
    data: { code, message: 'server sentence' },
  },
})

// The two temporary refusals mean "wait", never "wrong password", and the
// throttle says how long.
describe('signInErrorMessage', () => {
  it('says when to retry a throttled sign-in', () => {
    expect(
      signInErrorMessage(
        refusal(429, 'login_throttled', { 'retry-after': '17' }),
        'fb',
      ),
    ).toBe(
      'Too many failed sign-ins for this address. Try again in 17 seconds.',
    )
  })

  it('still says to wait when the header is missing', () => {
    expect(signInErrorMessage(refusal(429, 'login_throttled'), 'fb')).toBe(
      'Too many failed sign-ins for this address. Wait a minute and try again.',
    )
  })

  it('words a second-factor lockout', () => {
    expect(signInErrorMessage(refusal(429, 'two_factor_locked'), 'fb')).toBe(
      'Too many incorrect codes. Wait 15 minutes and try again.',
    )
  })

  it('keeps the server message for anything else', () => {
    expect(signInErrorMessage(refusal(401, 'invalid'), 'fb')).toBe(
      'server sentence',
    )
  })
})
