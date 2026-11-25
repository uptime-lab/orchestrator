import enLocale from '@/locales/en.json'
import { describe, expect, it } from 'vitest'

import type { Entitlement, EntitlementState } from './api'
import { stateLine } from './stateLine'

const fmt = (iso: string | undefined) => (iso ? iso.slice(0, 10) : '')

const base = (
  instance_operator: boolean,
  state: EntitlementState,
): Entitlement => ({
  instance_operator,
  state,
  free_device_cap: 25,
  effective_device_cap: 0,
  account_devices: 3,
  subscription_url: 'https://example.invalid/subscribe',
  expires_at: '2027-01-01T00:00:00Z',
  grace_until: '2027-01-31T00:00:00Z',
})

const card = enLocale.widgets.subscriptionCard as Record<string, unknown>

describe('stateLine for a non-operator', () => {
  it('names the install, not a limit, while covered', () => {
    expect(stateLine(base(false, 'active'), fmt, 'unlimited')).toEqual({
      key: 'tenantStateActive',
      values: { until: '2027-01-01' },
    })
  })

  it('says who renews in grace', () => {
    expect(stateLine(base(false, 'grace'), fmt, 'unlimited')).toEqual({
      key: 'tenantStateGrace',
      values: { expired: '2027-01-01', until: '2027-01-31' },
    })
  })

  it('carries no number when the install is limited', () => {
    for (const s of ['free', 'lapsed', 'rejected'] as const) {
      expect(stateLine(base(false, s), fmt, '25')).toEqual({
        key: 'tenantStateLimited',
        values: {},
      })
    }
  })

  it('every tenant key exists and names no cap', () => {
    for (const key of [
      'tenantStateActive',
      'tenantStateGrace',
      'tenantStateLimited',
    ]) {
      expect(typeof card[key]).toBe('string')
      expect(card[key] as string).not.toContain('{{cap}}')
    }
  })
})

describe('stateLine for the operator', () => {
  it('keeps the install terms with their numbers', () => {
    expect(stateLine(base(true, 'active'), fmt, 'unlimited')).toEqual({
      key: 'stateActive',
      values: { cap: 'unlimited', until: '2027-01-01' },
    })
    expect(stateLine(base(true, 'free'), fmt, '25')).toEqual({
      key: 'stateFree',
      values: { cap: 25 },
    })
    expect(stateLine(base(true, 'lapsed'), fmt, '25').key).toBe('stateLapsed')
    expect(stateLine(base(true, 'rejected'), fmt, '25').key).toBe(
      'stateRejected',
    )
    expect(stateLine(base(true, 'grace'), fmt, '25').key).toBe('stateGrace')
  })
})
