import { describe, expect, it } from 'vitest'

import '@/i18n'

import { describeAuditRetention } from './retention'

describe('describeAuditRetention', () => {
  it('states the retention in days', () => {
    expect(describeAuditRetention(365)).toBe('Kept for 365 days.')
    expect(describeAuditRetention(1)).toBe('Kept for 1 day.')
  })
  it('says indefinitely for 0', () => {
    expect(describeAuditRetention(0)).toBe('Kept indefinitely.')
  })
  it('claims nothing when the record did not say', () => {
    expect(describeAuditRetention(undefined)).toBeNull()
  })
})
