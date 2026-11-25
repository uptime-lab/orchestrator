import { describe, expect, it } from 'vitest'

import '@/i18n'

import { deviceCapReasonText } from './deviceCapText'

const answer = (status: number, data: Record<string, unknown>) => ({
  response: { status, data },
})

describe('deviceCapReasonText', () => {
  it('says the limit could not be checked, never that it was reached', () => {
    const text = deviceCapReasonText(
      answer(503, { code: 'device_cap_unverified', message: 'x' }),
    )
    expect(text).toBe(
      'The device limit could not be checked, so nothing was added. Try again in a moment.',
    )
  })

  it('words a reached limit from the values for the operator', () => {
    expect(
      deviceCapReasonText(
        answer(409, {
          code: 'device_cap_reached',
          data: { cap: 25, admitted: 25 },
        }),
      ),
    ).toBe(
      'This install already manages 25 devices and its entitlement covers 25. ' +
        'Devices already added keep working. Only adding this one is held. ' +
        'Remove a device you no longer need, or extend the entitlement.',
    )
  })

  it('gives a tenant the fact and its own count only', () => {
    expect(
      deviceCapReasonText(
        answer(409, {
          code: 'device_cap_reached',
          data: { instance_operator: false, account_devices: 3, cap: 25 },
        }),
      ),
    ).toBe(
      "This install's device limit is reached; ask its operator. " +
        'Devices already added keep working. Only adding this one is held. ' +
        'Your devices under management: 3.',
    )
  })

  it('leaves every other error to the server message', () => {
    expect(deviceCapReasonText(answer(409, { code: 'other' }))).toBeUndefined()
    expect(deviceCapReasonText(new Error('x'))).toBeUndefined()
  })
})
