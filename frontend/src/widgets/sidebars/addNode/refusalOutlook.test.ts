import { describe, expect, it } from 'vitest'

import '@/i18n'

import type { AdoptionRefusal } from '@/entities/node/api/adoptionRefusals'

import {
  REFUSAL_HISTORY_WINDOW_MS,
  refusalOutlook,
  visibleRefusals,
} from './refusalOutlook'

const now = new Date('2026-09-02T10:00:00Z')
const ago = (ms: number) => new Date(now.getTime() - ms).toISOString()

const refusal = (over: Partial<AdoptionRefusal> = {}): AdoptionRefusal => ({
  hardware_serial: '10000000abcd0002',
  first_seen_at: ago(23 * 60_000),
  last_seen_at: ago(2_000),
  occurrences: 690,
  live: true,
  still_bound_elsewhere: true,
  ...over,
})

describe('visibleRefusals', () => {
  it('always shows a device that is retrying right now, even if the serial was freed', () => {
    const rows = [refusal({ still_bound_elsewhere: false })]
    expect(visibleRefusals(rows, now)).toHaveLength(1)
  })

  it('keeps a quiet device on the list while its serial is still bound, within a week', () => {
    const rows = [
      refusal({ live: false, last_seen_at: ago(3 * 24 * 3_600_000) }),
    ]
    expect(visibleRefusals(rows, now)).toHaveLength(1)
  })

  it('drops a quiet device once the week is over', () => {
    const rows = [
      refusal({
        live: false,
        last_seen_at: ago(REFUSAL_HISTORY_WINDOW_MS + 1),
      }),
    ]
    expect(visibleRefusals(rows, now)).toHaveLength(0)
  })

  it('drops a quiet device whose serial has been freed: nothing is left to say', () => {
    const rows = [
      refusal({
        live: false,
        still_bound_elsewhere: false,
        last_seen_at: ago(60_000),
      }),
    ]
    expect(visibleRefusals(rows, now)).toHaveLength(0)
  })

  it('orders newest activity first', () => {
    const rows = [
      refusal({
        hardware_serial: '1000000000000001',
        last_seen_at: ago(60_000),
      }),
      refusal({
        hardware_serial: '1000000000000002',
        last_seen_at: ago(1_000),
      }),
    ]
    expect(visibleRefusals(rows, now).map((r) => r.hardware_serial)).toEqual([
      '1000000000000002',
      '1000000000000001',
    ])
  })
})

describe('refusalOutlook', () => {
  // The platform knows a serial binding. It does not know who holds it from
  // the tenant's side, nor that a device moved — so the copy must say neither.
  it('never names an owner or claims the device moved', () => {
    for (const r of [
      refusal(),
      refusal({ still_bound_elsewhere: false }),
      refusal({ live: false }),
    ]) {
      const { text } = refusalOutlook(r)
      expect(text).not.toMatch(/moved/i)
      expect(text).not.toMatch(/owner/i)
    }
  })

  it('tells a still-refused, still-retrying device apart from one whose serial was freed', () => {
    expect(refusalOutlook(refusal()).tone).toBe('error')
    expect(refusalOutlook(refusal()).text).toMatch(/another/i)
    const freed = refusalOutlook(refusal({ still_bound_elsewhere: false }))
    expect(freed.tone).toBe('warning')
    expect(freed.text).toMatch(/next/i)
  })

  it('dates a refusal that is no longer live', () => {
    const { text, tone } = refusalOutlook(refusal({ live: false }))
    expect(tone).toBe('warning')
    expect(text).toContain(new Date(refusal().last_seen_at).toLocaleString())
  })
})
