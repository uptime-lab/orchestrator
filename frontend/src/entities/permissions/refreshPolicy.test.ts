import { describe, expect, it } from 'vitest'

import {
  PERMISSIONS_REFRESH_INTERVAL_MS,
  shouldRefreshPermissions,
} from './refreshPolicy'

describe('shouldRefreshPermissions', () => {
  it('re-reads on return once the interval has passed', () => {
    expect(
      shouldRefreshPermissions(0, PERMISSIONS_REFRESH_INTERVAL_MS, true),
    ).toBe(true)
    expect(shouldRefreshPermissions(null, 1, true)).toBe(true)
  })

  it('does not re-read within the interval or when signed out', () => {
    expect(shouldRefreshPermissions(0, 1000, true)).toBe(false)
    expect(shouldRefreshPermissions(null, 1, false)).toBe(false)
  })
})
