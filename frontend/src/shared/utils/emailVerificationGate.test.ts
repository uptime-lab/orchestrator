import { describe, expect, it } from 'vitest'

import {
  EMAIL_VERIFICATION_PENDING_PATH,
  emailVerificationRedirect,
} from './emailVerificationGate'

describe('emailVerificationRedirect', () => {
  it('sends a refused unconfirmed open sign-up to the confirmation screen', () => {
    expect(
      emailVerificationRedirect(403, 'email_verification_required', '/'),
    ).toBe(EMAIL_VERIFICATION_PENDING_PATH)
  })

  it('does not loop on the confirmation screen itself', () => {
    expect(
      emailVerificationRedirect(
        403,
        'email_verification_required',
        '/verify-email',
      ),
    ).toBeNull()
  })

  it('leaves every other refusal alone', () => {
    expect(
      emailVerificationRedirect(403, 'two_factor_enrollment_required', '/'),
    ).toBeNull()
    expect(
      emailVerificationRedirect(401, 'email_verification_required', '/'),
    ).toBeNull()
  })
})
