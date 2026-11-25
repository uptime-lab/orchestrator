import { describe, expect, it } from 'vitest'

import { nodeIsBusy, overclockButtonState } from './overclockState'

const base = { applied: null, sending: false, nodeBusy: false }

describe('overclockButtonState', () => {
  it('asks for a choice when no preset is selected', () => {
    expect(overclockButtonState({ ...base, selected: null })).toBe('choose')
  })

  it('offers Apply for a selected preset', () => {
    expect(overclockButtonState({ ...base, selected: 'balanced' })).toBe(
      'apply',
    )
  })

  it('reads Sent only for the preset that was sent', () => {
    expect(
      overclockButtonState({
        ...base,
        selected: 'balanced',
        applied: 'balanced',
      }),
    ).toBe('sent')
    expect(
      overclockButtonState({ ...base, selected: 'stock', applied: 'balanced' }),
    ).toBe('apply')
  })

  it('holds a new preset while a command runs on the node', () => {
    expect(
      overclockButtonState({ ...base, selected: 'balanced', nodeBusy: true }),
    ).toBe('nodeBusy')
  })

  it('shows progress while sending', () => {
    expect(
      overclockButtonState({ ...base, selected: 'balanced', sending: true }),
    ).toBe('sending')
  })
})

describe('nodeIsBusy', () => {
  it('is true for maintenance and other transitions', () => {
    expect(nodeIsBusy('maintenance')).toBe(true)
    expect(nodeIsBusy('restarting')).toBe(true)
  })

  it('is false for online, offline and unknown', () => {
    expect(nodeIsBusy('online')).toBe(false)
    expect(nodeIsBusy('offline')).toBe(false)
    expect(nodeIsBusy(undefined)).toBe(false)
  })
})
