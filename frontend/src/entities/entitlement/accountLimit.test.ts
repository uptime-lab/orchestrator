import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'

import { accountLimitCard } from './accountLimit'
import type { AccountLimitEntitlement, EntitlementAnswer } from './api'

// The API module's client touches `window` when it loads.
let canApplyEntitlement: (e: EntitlementAnswer | null) => boolean
beforeAll(async () => {
  vi.stubGlobal('window', {})
  ;({ canApplyEntitlement } = await import('./api'))
})

afterAll(() => {
  vi.unstubAllGlobals()
})

const fmt = (iso: string | undefined) => (iso ? `D(${iso.slice(0, 10)})` : '')

const answer = (
  over: Partial<AccountLimitEntitlement> = {},
): AccountLimitEntitlement => ({
  instance_operator: false,
  mode: 'account_limit',
  account_devices: 3,
  account_device_limit: 5,
  account_plan: 'free',
  account_limit_contact: 'sales@example.com',
  ...over,
})

describe('accountLimitCard', () => {
  it('is a plain free plan with no date', () => {
    const card = accountLimitCard(answer(), fmt, '?')
    expect(card.plan).toEqual({
      key: 'freePlan',
      values: { devices: 3, limit: 5 },
    })
    expect(card.remedy).toEqual({
      key: 'freeMore',
      values: { contact: 'sales@example.com' },
    })
    expect(card.ends).toBeNull()
    expect(JSON.stringify(card)).not.toContain('D(')
  })

  it('shows a dated subscription with its end', () => {
    const card = accountLimitCard(
      answer({
        account_plan: 'subscription',
        account_device_limit: 50,
        account_devices: 12,
        account_plan_until: '2027-06-30T00:00:00Z',
      }),
      fmt,
      '?',
    )
    expect(card.plan).toEqual({
      key: 'subscriptionPlanUntil',
      values: { devices: 12, limit: 50, until: 'D(2027-06-30)' },
    })
    expect(card.remedy?.key).toBe('subscriptionMore')
    expect(card.ends?.key).toBe('subscriptionEnds')
  })

  it('a subscription with no end carries no date', () => {
    const card = accountLimitCard(
      answer({ account_plan: 'subscription', account_device_limit: 50 }),
      fmt,
      '?',
    )
    expect(card.plan.key).toBe('subscriptionPlan')
    expect(card.ends).toBeNull()
  })

  it('after a subscription ends, an account over the free limit is told its devices keep working', () => {
    const card = accountLimitCard(answer({ account_devices: 25 }), fmt, '?')
    expect(card.plan).toEqual({
      key: 'freePlan',
      values: { devices: 25, limit: 5 },
    })
    expect(card.remedy?.key).toBe('overLimit')
  })

  it('names nobody when no contact is configured', () => {
    const card = accountLimitCard(
      answer({ account_limit_contact: undefined }),
      fmt,
      '?',
    )
    expect(card.remedy?.key).toBe('freeMoreNoContact')
    expect(card.namesContact).toBe(false)
  })

  it('says the limit is unknown rather than inventing one', () => {
    const card = accountLimitCard(
      answer({ account_device_limit: -1, account_plan: undefined }),
      fmt,
      '?',
    )
    expect(card.plan.key).toBe('limitUnknown')
    expect(card.remedy).toBeNull()
  })
})

describe('canApplyEntitlement', () => {
  it('keeps the paste away from an account-limit answer', () => {
    expect(canApplyEntitlement(answer())).toBe(false)
    expect(canApplyEntitlement(null)).toBe(false)
    const operator = {
      instance_operator: true,
      state: 'active',
      free_device_cap: 25,
      effective_device_cap: 0,
      subscription_url: 'https://example.com',
    } as EntitlementAnswer
    expect(canApplyEntitlement(operator)).toBe(true)
    expect(
      canApplyEntitlement({
        ...operator,
        instance_operator: false,
      } as EntitlementAnswer),
    ).toBe(false)
  })
})
