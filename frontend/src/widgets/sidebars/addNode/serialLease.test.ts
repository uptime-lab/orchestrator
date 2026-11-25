import { describe, expect, it } from 'vitest'

import { serialLeaseHint } from './serialLease'

describe('serialLeaseHint', () => {
  it('names the configured lease', () => {
    expect(serialLeaseHint(30)).toEqual({ kind: 'days', days: 30 })
    expect(serialLeaseHint(7)).toEqual({ kind: 'days', days: 7 })
  })

  it('says nothing when the server never releases a serial', () => {
    expect(serialLeaseHint(0)).toEqual({ kind: 'none' })
  })

  it('falls back to the default wording for a server that does not report it', () => {
    expect(serialLeaseHint(undefined)).toEqual({ kind: 'unknown' })
    expect(serialLeaseHint(null)).toEqual({ kind: 'unknown' })
  })
})
