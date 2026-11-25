import { describe, expect, it } from 'vitest'

import {
  RECONNECT_BASE_DELAY_MS,
  RECONNECT_MAX_DELAY_MS,
  reconnectDelayMs,
} from './reconnectBackoff'

describe('reconnectDelayMs', () => {
  const noJitter = () => 0

  it('doubles from the base delay', () => {
    expect(reconnectDelayMs(0, noJitter)).toBe(RECONNECT_BASE_DELAY_MS)
    expect(reconnectDelayMs(1, noJitter)).toBe(2 * RECONNECT_BASE_DELAY_MS)
    expect(reconnectDelayMs(3, noJitter)).toBe(8 * RECONNECT_BASE_DELAY_MS)
  })

  it('never exceeds the ceiling, however many attempts', () => {
    for (const attempt of [5, 10, 100, 10_000, Number.MAX_SAFE_INTEGER]) {
      const delay = reconnectDelayMs(attempt, noJitter)
      expect(delay).toBe(RECONNECT_MAX_DELAY_MS)
      expect(Number.isFinite(delay)).toBe(true)
    }
  })

  it('jitters downward only', () => {
    const full = reconnectDelayMs(20, () => 0.999)
    expect(full).toBeLessThanOrEqual(RECONNECT_MAX_DELAY_MS)
    expect(full).toBeGreaterThanOrEqual(RECONNECT_MAX_DELAY_MS * 0.8)
  })
})
