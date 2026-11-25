import { describe, expect, it } from 'vitest'

import type { Entitlement } from './api'
import { freeFloorClamp } from './freeFloor'

const base: Entitlement = {
  instance_operator: true,
  state: 'active',
  free_device_cap: 25,
  effective_device_cap: 25,
  admitted_devices: 3,
  subscription_url: '',
  device_cap: 10,
}

describe('freeFloorClamp', () => {
  it('reports the signed and the free cap when the file was clamped', () => {
    expect(
      freeFloorClamp({ ...base, device_cap_below_free_floor: true }),
    ).toEqual({ signedCap: 10, freeCap: 25 })
  })

  it('says nothing when the flag is absent or false', () => {
    expect(freeFloorClamp(base)).toBeNull()
    expect(
      freeFloorClamp({ ...base, device_cap_below_free_floor: false }),
    ).toBeNull()
  })

  it('says nothing outside the operator view', () => {
    expect(
      freeFloorClamp({
        ...base,
        instance_operator: false,
        device_cap_below_free_floor: true,
      }),
    ).toBeNull()
  })
})
