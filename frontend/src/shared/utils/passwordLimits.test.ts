import { describe, expect, it } from 'vitest'

import {
  MAX_PASSWORD_BYTES,
  passwordByteLength,
  passwordTooLong,
} from './passwordLimits'

describe('passwordTooLong', () => {
  it('accepts exactly the byte limit', () => {
    expect(passwordTooLong('a'.repeat(MAX_PASSWORD_BYTES))).toBe(false)
  })

  it('refuses one byte over the limit', () => {
    expect(passwordTooLong('a'.repeat(MAX_PASSWORD_BYTES + 1))).toBe(true)
  })

  it('counts UTF-8 bytes, not characters', () => {
    // Cyrillic letters are two bytes each: 37 characters, 74 bytes.
    const cyrillic = 'ж'.repeat(37)
    expect(cyrillic.length).toBeLessThan(MAX_PASSWORD_BYTES)
    expect(passwordByteLength(cyrillic)).toBe(74)
    expect(passwordTooLong(cyrillic)).toBe(true)
  })
})
