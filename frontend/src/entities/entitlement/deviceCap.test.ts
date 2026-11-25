import { describe, expect, it } from 'vitest'

import {
  deviceCapNotice,
  deviceCapRefusal,
  deviceCapTenantDetail,
  isDeviceCapUnverified,
} from './deviceCap'

const refusal = (data: Record<string, unknown>) => ({
  response: {
    status: 409,
    data: {
      success: false,
      message: 'device limit reached',
      code: 'device_cap_reached',
      data,
    },
  },
})

describe('deviceCapRefusal', () => {
  it('reads the cap, the count and the link out of the typed 409', () => {
    expect(
      deviceCapRefusal(
        refusal({
          cap: 25,
          count: 25,
          subscription_url: 'https://example.com/plans',
        }),
      ),
    ).toMatchObject({
      instanceOperator: true,
      cap: 25,
      count: 25,
      subscriptionUrl: 'https://example.com/plans',
    })
  })

  it('reads the operator shape with instance_operator and admitted', () => {
    expect(
      deviceCapRefusal(
        refusal({
          instance_operator: true,
          admitted: 25,
          cap: 25,
          account_devices: 4,
          subscription_url: 'https://example.com/plans',
        }),
      ),
    ).toEqual({
      instanceOperator: true,
      cap: 25,
      count: 25,
      accountDevices: 4,
      subscriptionUrl: 'https://example.com/plans',
    })
  })

  it('keeps every install-wide number and the link from a non-operator', () => {
    // Even if a server still sent them, a tenant never reads them.
    const r = deviceCapRefusal(
      refusal({
        instance_operator: false,
        account_devices: 3,
        cap: 25,
        count: 25,
        admitted: 25,
        subscription_url: 'https://example.com/plans',
      }),
    )
    expect(r).toEqual({
      instanceOperator: false,
      cap: 0,
      count: 0,
      accountDevices: 3,
      subscriptionUrl: '',
    })
  })

  it('is null for every other refusal, including another 409', () => {
    expect(deviceCapRefusal(undefined)).toBeNull()
    expect(deviceCapRefusal(new Error('Network Error'))).toBeNull()
    expect(
      deviceCapRefusal({
        response: { status: 409, data: { code: 'serial_bound_elsewhere' } },
      }),
    ).toBeNull()
  })

  it('tolerates a refusal that carries the code alone', () => {
    expect(
      deviceCapRefusal({ response: { data: { code: 'device_cap_reached' } } }),
    ).toEqual({
      instanceOperator: true,
      cap: 0,
      count: 0,
      accountDevices: null,
      subscriptionUrl: '',
    })
  })
})

describe('deviceCapNotice', () => {
  it('gives the operator the numbers, the remedy and the link', () => {
    const r = deviceCapRefusal(
      refusal({ cap: 25, count: 25, subscription_url: 'https://e.x/p' }),
    )!
    expect(deviceCapNotice(r)).toEqual({
      title: { key: 'title', values: { admitted: 25, cap: 25 } },
      lines: [{ key: 'fleetKeepsWorking' }, { key: 'remedy' }],
      subscriptionUrl: 'https://e.x/p',
    })
  })

  it('gives a non-operator the fact and its own count, no number, no link', () => {
    const r = deviceCapRefusal(
      refusal({
        instance_operator: false,
        account_devices: 3,
        subscription_url: 'https://e.x/p',
      }),
    )!
    const model = deviceCapNotice(r)
    expect(model).toEqual({
      title: { key: 'titleTenant' },
      lines: [
        { key: 'fleetKeepsWorking' },
        { key: 'accountDevices', values: { devices: 3 } },
      ],
      subscriptionUrl: '',
    })
  })
})

describe('deviceCapTenantDetail', () => {
  it('is null for the operator and for an older backend', () => {
    expect(
      deviceCapTenantDetail({
        instance_operator: true,
        current: '25',
        expected: '25',
      }),
    ).toBeNull()
    expect(deviceCapTenantDetail({ current: '25', expected: '25' })).toBeNull()
  })

  it('gives a non-operator the fact and its own count only', () => {
    expect(
      deviceCapTenantDetail({ instance_operator: false, account_devices: 3 }),
    ).toEqual([
      { key: 'titleTenant' },
      { key: 'accountDevices', values: { devices: 3 } },
    ])
    expect(
      deviceCapTenantDetail({ instance_operator: false, account_devices: -1 }),
    ).toEqual([{ key: 'titleTenant' }])
  })
})

describe('isDeviceCapUnverified', () => {
  const unverified = {
    response: {
      status: 503,
      data: {
        success: false,
        message: 'The device limit could not be checked.',
        code: 'device_cap_unverified',
      },
    },
  }

  it('recognises the 503 the server sends when it cannot check the cap', () => {
    expect(isDeviceCapUnverified(unverified)).toBe(true)
  })

  it('is never read as the limit being reached', () => {
    expect(deviceCapRefusal(unverified)).toBeNull()
    expect(isDeviceCapUnverified(refusal({ cap: 25, count: 25 }))).toBe(false)
  })

  it('ignores anything else', () => {
    expect(isDeviceCapUnverified(new Error('network'))).toBe(false)
    expect(isDeviceCapUnverified(undefined)).toBe(false)
  })
})

const accountRefusal = (data: Record<string, unknown>) => ({
  response: {
    status: 409,
    data: {
      success: false,
      message: 'account limit',
      code: 'account_device_limit_reached',
      data,
    },
  },
})

describe('the account-limit refusal', () => {
  it('is read as the account limit, with the account numbers and contact', () => {
    const r = deviceCapRefusal(
      accountRefusal({
        limit: 5,
        account_devices: 5,
        plan: 'free',
        contact: 'sales@example.com',
      }),
    )
    expect(r?.accountLimit).toEqual({
      limit: 5,
      devices: 5,
      plan: 'free',
      until: '',
      contact: 'sales@example.com',
    })
    expect(r?.subscriptionUrl).toBe('')
    const model = deviceCapNotice(r!)
    expect(model.title).toEqual({
      key: 'accountTitle',
      values: { devices: 5, limit: 5 },
    })
    expect(model.lines.map((l) => l.key)).toEqual([
      'fleetKeepsWorking',
      'accountRemedy',
    ])
    expect(model.subscriptionUrl).toBe('')
    expect(model.contact).toBe('sales@example.com')
  })

  it('names nobody without a contact, and never the install limit', () => {
    const model = deviceCapNotice(
      deviceCapRefusal(accountRefusal({ limit: 50, account_devices: 50 }))!,
    )
    expect(model.lines.map((l) => l.key)).toContain('accountRemedyNoContact')
    expect(model.title.key).not.toBe('titleTenant')
  })
})
