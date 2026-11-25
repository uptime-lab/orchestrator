import { renderToStaticMarkup } from 'react-dom/server'
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'

import type { AccountLimitEntitlement } from '@/entities/entitlement/api'

// The Subscription page of an account the per-account device limit binds:
// its own plan, never the install's entitlement or its dates.
const render = async (e: AccountLimitEntitlement) => {
  const { AccountLimitSummary } = await import('./index.tsx')
  return renderToStaticMarkup(<AccountLimitSummary entitlement={e} />)
}

// The API client touches `window` when it loads.
beforeAll(async () => {
  vi.stubGlobal('window', {})
  await import('@/i18n')
})

afterAll(() => {
  vi.unstubAllGlobals()
})

const free: AccountLimitEntitlement = {
  instance_operator: false,
  mode: 'account_limit',
  account_devices: 3,
  account_device_limit: 5,
  account_plan: 'free',
  account_limit_contact: 'sales@example.com',
}

describe('AccountLimitSummary', () => {
  it('renders a plain Free plan with the count, no date and a mailto contact', async () => {
    const html = await render(free)
    expect(html).toContain('Free: 3 of 5 devices.')
    expect(html).toContain('More devices need a subscription.')
    expect(html).toContain('href="mailto:sales@example.com"')
    expect(html).not.toMatch(/until|expire|valid/i)
  })

  it('renders a dated subscription with its end', async () => {
    const html = await render({
      ...free,
      account_plan: 'subscription',
      account_device_limit: 50,
      account_devices: 12,
      account_plan_until: '2027-06-30T00:00:00Z',
    })
    expect(html).toContain('Subscription: 12 of 50 devices, until')
    expect(html).toContain('returns to the free limit')
  })

  it('tells an account over its limit that its devices keep working', async () => {
    const html = await render({ ...free, account_devices: 25 })
    expect(html).toContain('Free: 25 of 5 devices.')
    expect(html).toContain('Devices you already have keep working.')
  })

  it('renders a contact that is not an address as text only', async () => {
    const html = await render({
      ...free,
      account_limit_contact: 'your account manager',
    })
    expect(html).toContain('your account manager')
    expect(html).not.toContain('<a')
  })
})
