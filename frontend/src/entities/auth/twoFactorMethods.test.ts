import { describe, expect, it } from 'vitest'

import { normalizeTwoFactorMethods } from './twoFactorMethods.ts'

describe('normalizeTwoFactorMethods', () => {
  it('keeps what the server offered', () => {
    expect(normalizeTwoFactorMethods(['passkey'])).toEqual(['passkey'])
    expect(normalizeTwoFactorMethods(['totp', 'passkey'])).toEqual([
      'totp',
      'passkey',
    ])
  })

  it('falls back to a code for an older backend that names no methods', () => {
    expect(normalizeTwoFactorMethods(undefined)).toEqual(['totp'])
    expect(normalizeTwoFactorMethods(null)).toEqual(['totp'])
  })

  it('never renders an empty step', () => {
    expect(normalizeTwoFactorMethods([])).toEqual(['totp'])
  })
})
