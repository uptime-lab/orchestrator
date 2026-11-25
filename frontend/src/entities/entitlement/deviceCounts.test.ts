import { describe, expect, it } from 'vitest'

import type { Entitlement } from './api'
import { deviceCountLines } from './deviceCounts'

const operator: Entitlement = {
  instance_operator: true,
  state: 'active',
  free_device_cap: 25,
  effective_device_cap: 0,
  admitted_devices: 97,
  account_devices: 4,
  subscription_url: 'https://example.invalid/subscribe',
}

const tenant: Entitlement = {
  instance_operator: false,
  state: 'active',
  free_device_cap: 25,
  effective_device_cap: 0,
  account_devices: 3,
  subscription_url: 'https://example.invalid/subscribe',
}

describe('deviceCountLines', () => {
  it('shows the operator the install count, its own count and the link', () => {
    expect(deviceCountLines(operator)).toEqual({
      install: 97,
      account: 4,
      subscriptionLink: true,
    })
  })

  it('shows a tenant only its own count and no link', () => {
    expect(deviceCountLines(tenant)).toEqual({
      install: null,
      account: 3,
      subscriptionLink: false,
    })
  })

  it('shows a tenant no count when an older backend omits account_devices', () => {
    // An older backend sent the install-wide count to everyone; it must not
    // be shown to a tenant as if it were the tenant's own.
    const { account_devices: _omitted, ...rest } = tenant
    const older: Entitlement = { ...rest, admitted_devices: 97 }
    expect(deviceCountLines(older)).toEqual({
      install: null,
      account: null,
      subscriptionLink: false,
    })
  })

  it('says unknown for a count the server could not read', () => {
    expect(
      deviceCountLines({
        ...operator,
        admitted_devices: -1,
        account_devices: -1,
      }),
    ).toEqual({
      install: 'unknown',
      account: 'unknown',
      subscriptionLink: true,
    })
  })
})
