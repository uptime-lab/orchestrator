import { describe, expect, it } from 'vitest'

import { isNodeDispatchable, isNodeHeldForWork } from './utils'

// "restarting" answers two different questions with one word. The platform pins
// a node at DISPATCH for every command that MIGHT take the device down, and for
// a bootloader flash that claim is usually false: the device stays up and an
// operator is told the machine is going down when it is not. pinned_by_slug is
// what lets the UI tell the two apart.
describe('isNodeHeldForWork', () => {
  it('reads a pin imposed by a command that never asked for a reboot', () => {
    expect(isNodeHeldForWork('restarting', 'update_boot_order')).toBe(true)
    expect(isNodeHeldForWork('restarting', 'enable_eeprom')).toBe(true)
  })

  it('leaves a real reboot alone', () => {
    expect(isNodeHeldForWork('restarting', 'reboot')).toBe(false)
  })

  // Absence is not evidence: a node pinned by an older backend carries no slug,
  // and softening the word on a guess is the mistake in the other direction.
  it('says nothing without a recorded cause', () => {
    expect(isNodeHeldForWork('restarting', undefined)).toBe(false)
    expect(isNodeHeldForWork('restarting', '')).toBe(false)
  })

  // "Maintenance" is already calm and accurate, so it is left as it is; and a
  // stale slug on a settled node must never make it read as busy.
  it('applies to no other status', () => {
    expect(isNodeHeldForWork('maintenance', 'apply_utility_config')).toBe(false)
    expect(isNodeHeldForWork('online', 'update_boot_order')).toBe(false)
    expect(isNodeHeldForWork('offline', 'update_boot_order')).toBe(false)
  })
})

// Mirrors the server's dispatch guard: a non-queueable command is accepted for
// an online or a maintenance node and refused for anything else.
describe('isNodeDispatchable', () => {
  it('matches what the platform would accept', () => {
    expect(isNodeDispatchable('online')).toBe(true)
    expect(isNodeDispatchable('maintenance')).toBe(true)
    expect(isNodeDispatchable('restarting')).toBe(false)
    expect(isNodeDispatchable('offline')).toBe(false)
    expect(isNodeDispatchable('shutdown')).toBe(false)
    expect(isNodeDispatchable(undefined)).toBe(false)
  })
})
