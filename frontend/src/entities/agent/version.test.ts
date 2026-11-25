import { describe, expect, it } from 'vitest'

import { agentUpdateState, agentVersionLess } from './version'

describe('agentVersionLess', () => {
  it('orders numerically, segment by segment', () => {
    expect(agentVersionLess('0.2.9', '0.2.10')).toBe(true)
    expect(agentVersionLess('0.2.10', '0.2.9')).toBe(false)
    expect(agentVersionLess('0.7.8', '0.8.0')).toBe(true)
    expect(agentVersionLess('1.0.0', '1.0.0')).toBe(false)
  })

  it('sorts a prefix lower, and a non-number lexicographically', () => {
    expect(agentVersionLess('0.7', '0.7.1')).toBe(true)
    expect(agentVersionLess('0.7.1-rc1', '0.7.1-rc2')).toBe(true)
  })
})

describe('agentUpdateState', () => {
  it('offers an update only when latest is strictly newer', () => {
    expect(agentUpdateState('0.7.8', '0.7.9')).toBe('update')
    expect(agentUpdateState('0.7.9', '0.7.10')).toBe('update')
  })

  it('reads a node on latest as current', () => {
    expect(agentUpdateState('0.7.9', '0.7.9')).toBe('current')
  })

  // A rollback, or an import that left latest behind: the node is newer than
  // what an update installs, and "Update" would be a downgrade.
  it('offers nothing to a node newer than latest', () => {
    expect(agentUpdateState('0.8.0', '0.7.9')).toBe('ahead')
    expect(agentUpdateState('0.7.10', '0.7.9')).toBe('ahead')
  })

  it('keeps the row as before when either side is unknown', () => {
    expect(agentUpdateState('0.7.9', null)).toBe('update')
    expect(agentUpdateState(undefined, '0.7.9')).toBe('update')
    expect(agentUpdateState('  ', '0.7.9')).toBe('update')
  })
})
