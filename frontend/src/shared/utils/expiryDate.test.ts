import { describe, expect, it } from 'vitest'

import {
  earliestExpiryDay,
  expiryEndOfDay,
  isSelectableExpiryDay,
} from './expiryDate'

describe('expiry day picker', () => {
  const now = new Date(2030, 0, 31, 15, 0, 0)

  it('offers tomorrow as the earliest day', () => {
    expect(earliestExpiryDay(now)).toBe('2030-02-01')
    expect(earliestExpiryDay(new Date(2030, 11, 31, 23, 59))).toBe('2031-01-01')
  })

  it('sends the end of the chosen day, never its start', () => {
    const iso = expiryEndOfDay('2030-02-01')
    expect(iso).not.toBeNull()
    expect(iso).toBe(new Date(2030, 1, 1, 23, 59, 59, 999).toISOString())
    expect(new Date(iso as string).getTime()).toBeGreaterThan(now.getTime())
  })

  it('refuses today, the past and a malformed day', () => {
    expect(isSelectableExpiryDay('2030-01-31', now)).toBe(false)
    expect(isSelectableExpiryDay('2029-12-31', now)).toBe(false)
    expect(isSelectableExpiryDay('not-a-day', now)).toBe(false)
    expect(isSelectableExpiryDay('2030-02-01', now)).toBe(true)
  })
})
