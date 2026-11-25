import { describe, expect, it } from 'vitest'

import {
  markOidcFlowStarted,
  OIDC_FLOW_MAX_AGE_MS,
  oidcCodeStartedHere,
  takeOidcFlowMarker,
} from './oidcFlowMarker'

const memoryStorage = () => {
  const data = new Map<string, string>()
  return {
    data,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => {
      data.set(k, v)
    },
    removeItem: (k: string) => {
      data.delete(k)
    },
  }
}

describe('OIDC flow marker', () => {
  it('accepts a code in the tab that started the flow', () => {
    const s = memoryStorage()
    markOidcFlowStarted(s, () => 1_000)
    expect(takeOidcFlowMarker(s, () => 2_000)).toBe(true)
  })

  it('refuses a code in a tab that started nothing', () => {
    expect(takeOidcFlowMarker(memoryStorage(), () => 0)).toBe(false)
  })

  it('is single use', () => {
    const s = memoryStorage()
    markOidcFlowStarted(s, () => 1_000)
    expect(takeOidcFlowMarker(s, () => 1_001)).toBe(true)
    expect(takeOidcFlowMarker(s, () => 1_002)).toBe(false)
  })

  it('refuses a stale marker and clears it', () => {
    const s = memoryStorage()
    markOidcFlowStarted(s, () => 0)
    expect(takeOidcFlowMarker(s, () => OIDC_FLOW_MAX_AGE_MS + 1)).toBe(false)
    expect(s.data.size).toBe(0)
  })

  it('refuses when storage is unavailable', () => {
    expect(takeOidcFlowMarker(null)).toBe(false)
  })

  it('answers the same for one code asked twice', () => {
    const s = memoryStorage()
    markOidcFlowStarted(s, () => 1_000)
    expect(oidcCodeStartedHere('code-1', s, () => 1_001)).toBe(true)
    expect(oidcCodeStartedHere('code-1', s, () => 1_002)).toBe(true)
    expect(oidcCodeStartedHere('code-2', s, () => 1_003)).toBe(false)
  })
})
