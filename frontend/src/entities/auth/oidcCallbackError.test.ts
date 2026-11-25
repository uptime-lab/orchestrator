import { describe, expect, it } from 'vitest'

import '@/i18n'

import { oidcCallbackErrorText } from './oidcCallbackError'

describe('oidcCallbackErrorText', () => {
  // A provider account still held by another user's earlier link cannot be
  // fixed by trying again, so it is not worded as the generic failure.
  it('says what to do when the provider account is linked to another user', () => {
    const text = oidcCallbackErrorText('identity_conflict', 'Google')
    expect(text).not.toBe(oidcCallbackErrorText('login_failed', 'Google'))
    expect(text).toContain('Google')
    expect(text).toMatch(/administrator/)
  })

  it('reads an unknown code as the generic failure', () => {
    expect(oidcCallbackErrorText('something_new', 'Google')).toBe(
      'Sign-in failed. Please try again.',
    )
  })
})
