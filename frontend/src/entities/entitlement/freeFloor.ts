import type { Entitlement } from './api'

export type FreeFloorClamp = {
  signedCap: number
  freeCap: number
}

// freeFloorClamp says whether the file in force signs a device cap below the
// free cap, which the server enforces as the free cap instead
// (device_cap_below_free_floor). Only the operator view carries the flag and
// the signed cap it explains; any other view returns null.
export const freeFloorClamp = (e: Entitlement): FreeFloorClamp | null => {
  if (!e.instance_operator || !e.device_cap_below_free_floor) return null
  return { signedCap: e.device_cap ?? 0, freeCap: e.free_device_cap }
}
