import enLocale from '@/locales/en.json'
import { describe, expect, it } from 'vitest'

import type { AppliedEntitlement, ApplyEntitlementResult } from './api'
import { applyOutcome } from './applyOutcome'

const fmt = (iso: string | undefined) => (iso ? iso.slice(0, 10) : '')

const inForce: ApplyEntitlementResult = {
  instance_operator: true,
  state: 'active',
  source: 'database',
  free_device_cap: 25,
  effective_device_cap: 100,
  subscription_url: 'https://example.invalid/subscribe',
  expires_at: '2028-01-01T00:00:00Z',
}

const withApplied = (
  applied: Partial<AppliedEntitlement>,
): ApplyEntitlementResult => ({
  ...inForce,
  applied: {
    in_force: true,
    issued_at: '2027-01-01T00:00:00Z',
    expires_at: '2027-06-01T00:00:00Z',
    device_cap: 50,
    state: 'active',
    ...applied,
  },
})

const card = enLocale.widgets.subscriptionCard as Record<string, unknown>

describe('applyOutcome', () => {
  it('is a plain success when the backend says nothing about the paste', () => {
    expect(applyOutcome(inForce, fmt)).toEqual({
      tone: 'success',
      key: 'applied',
      values: {},
    })
  })

  it('is a plain success when the pasted file is the one in force', () => {
    expect(applyOutcome(withApplied({ in_force: true }), fmt).key).toBe(
      'applied',
    )
  })

  it('says another file is in force when the paste is outranked', () => {
    expect(applyOutcome(withApplied({ in_force: false }), fmt)).toEqual({
      tone: 'notice',
      key: 'appliedNotInForce',
      values: { expires: '2027-06-01' },
    })
  })

  it('says the file has expired when the paste is lapsed', () => {
    expect(
      applyOutcome(withApplied({ in_force: false, state: 'lapsed' }), fmt),
    ).toEqual({
      tone: 'notice',
      key: 'appliedLapsed',
      values: { expires: '2027-06-01' },
    })
  })

  it('names only keys the locale carries, with the values it interpolates', () => {
    for (const r of [
      inForce,
      withApplied({ in_force: false }),
      withApplied({ in_force: false, state: 'lapsed' }),
    ]) {
      const { key, values } = applyOutcome(r, fmt)
      expect(typeof card[key]).toBe('string')
      const placeholders = [...(card[key] as string).matchAll(/{{(\w+)}}/g)]
        .map((m) => m[1])
        .sort()
      expect(placeholders).toEqual(Object.keys(values).sort())
    }
  })
})
