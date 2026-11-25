import { describe, expect, it } from 'vitest'

import {
  dismissalValue,
  isDismissed,
  undismissedCountsByKind,
  undismissedTotal,
  withDismissal,
  withoutDismissal,
  type Dismissals,
} from './dismissals'

const item = (
  over: Partial<{
    kind: string
    expected: string
    subject: string
    count: number
  }> = {},
) => ({
  kind: over.kind ?? 'agent_outdated',
  severity: 'warning' as const,
  expected: over.expected,
  subject: over.subject,
  count: over.count,
})

// The whole feature. A dismissal keyed to a clock hides a real problem for as
// long as nobody is watching; keyed to the VALUE, it comes back the moment the
// situation changes — the rule markDismissal already settled for the button
// mark.
describe('a dismissal is keyed to what was dismissed', () => {
  it('hides the occurrence it was made against', () => {
    const d = withDismissal({}, 'n1', item({ expected: '0.7.1' }))
    expect(isDismissed(d, 'n1', item({ expected: '0.7.1' }))).toBe(true)
  })

  it('does not hide a later one', () => {
    const d = withDismissal({}, 'n1', item({ expected: '0.7.1' }))
    expect(isDismissed(d, 'n1', item({ expected: '0.8.0' }))).toBe(false)
  })

  it('is scoped to one node and one condition', () => {
    const d = withDismissal({}, 'n1', item({ expected: '0.7.1' }))
    expect(isDismissed(d, 'n2', item({ expected: '0.7.1' }))).toBe(false)
    expect(
      isDismissed(
        d,
        'n1',
        item({ kind: 'bootloader_outdated', expected: '0.7.1' }),
      ),
    ).toBe(false)
  })

  it('can be taken back', () => {
    let d: Dismissals = withDismissal({}, 'n1', item({ expected: '0.7.1' }))
    d = withoutDismissal(d, 'n1', 'agent_outdated')
    expect(isDismissed(d, 'n1', item({ expected: '0.7.1' }))).toBe(false)
  })
})

describe('the dismissed value', () => {
  it('prefers the thing that moves', () => {
    expect(dismissalValue(item({ expected: '0.7.1' }))).toBe('0.7.1')
    expect(dismissalValue(item({ subject: 'docker' }))).toBe('docker')
    expect(dismissalValue(item({ count: 3 }))).toBe('3')
  })

  it('falls back to a bare fact for a condition with no value', () => {
    // in_recovery and "declared, never applied" carry nothing that moves, so
    // they dismiss as themselves and return when they clear and recur.
    expect(dismissalValue(item({ kind: 'in_recovery' }))).toBe('set')
  })
})

// The number in the nav and the numbers on the filter chips come from this one
// function, and they have to agree: split in two, the visible failure is "I
// hid it and the number did not move", which reads as a broken button.
describe('counting what is not hidden', () => {
  const nodes = [
    {
      node_id: 'n1',
      items: [item({ expected: '0.7.1' }), item({ kind: 'offline' })],
    },
    { node_id: 'n2', items: [item({ expected: '0.7.1' })] },
  ]

  it('counts everything when nothing is hidden', () => {
    expect(undismissedCountsByKind(nodes, {})).toEqual({
      agent_outdated: 2,
      offline: 1,
    })
    expect(undismissedTotal(nodes, {})).toBe(3)
  })

  it('drops only the node and condition that were hidden', () => {
    const d = withDismissal({}, 'n1', item({ expected: '0.7.1' }))
    expect(undismissedCountsByKind(nodes, d)).toEqual({
      agent_outdated: 1,
      offline: 1,
    })
    expect(undismissedTotal(nodes, d)).toBe(2)
  })

  it('counts a hidden condition again once its value moves', () => {
    const d = withDismissal({}, 'n2', item({ expected: '0.7.1' }))
    const later = [{ node_id: 'n2', items: [item({ expected: '0.8.0' })] }]
    expect(undismissedTotal(later, d)).toBe(1)
  })

  it('is zero, not absent, when every condition is hidden', () => {
    let d: Dismissals = {}
    for (const n of nodes) {
      for (const i of n.items) {
        d = withDismissal(d, n.node_id, i)
      }
    }
    expect(undismissedTotal(nodes, d)).toBe(0)
  })
})
