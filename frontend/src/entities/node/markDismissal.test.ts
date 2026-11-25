import { describe, expect, it } from 'vitest'

import { markTimestamp, shouldHydrateMark } from './markDismissal'

describe('shouldHydrateMark', () => {
  it('shows a mark this browser has never dismissed', () => {
    expect(shouldHydrateMark(undefined, 1000)).toBe(true)
  })

  // The case the whole thing exists for: the off-click dispatches nothing, so
  // the registry keeps the mark and replays it on the next reconnect.
  it('suppresses the exact mark that was dismissed', () => {
    expect(shouldHydrateMark(1000, 1000)).toBe(false)
  })

  it('suppresses an older mark, which cannot be newer than the dismissal', () => {
    expect(shouldHydrateMark(1000, 900)).toBe(false)
  })

  // A dismissal must not swallow the NEXT press. Somebody walked to the rack.
  it('shows a newer press', () => {
    expect(shouldHydrateMark(1000, 1001)).toBe(true)
  })
})

describe('markTimestamp', () => {
  it('uses the platform instant, so both sides name the same press', () => {
    expect(markTimestamp('2026-08-20T10:00:00Z', 999)).toBe(
      new Date('2026-08-20T10:00:00Z').getTime(),
    )
  })

  // An older backend sends nothing; the mark still works, it just cannot be
  // compared with a hydrated one — the behaviour this replaces.
  it('falls back to the local clock', () => {
    expect(markTimestamp(undefined, 999)).toBe(999)
    expect(markTimestamp('not a date', 999)).toBe(999)
  })
})
