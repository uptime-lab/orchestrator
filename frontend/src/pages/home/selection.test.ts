import { describe, expect, it } from 'vitest'

import { isStaleTargetRefusal, pruneSelection } from './selection'

describe('pruneSelection', () => {
  const nodes = [{ id: 'a' }, { id: 'b' }]

  it('leaves out a selected node that is no longer in the list', () => {
    expect(pruneSelection(new Set(['a', 'gone', 'b']), nodes)).toEqual({
      kept: ['a', 'b'],
      dropped: 1,
    })
  })

  it('keeps a selection that still names only existing nodes', () => {
    expect(pruneSelection(['b'], nodes)).toEqual({ kept: ['b'], dropped: 0 })
  })

  it('keeps nothing when every selected node is gone', () => {
    expect(pruneSelection(['x', 'y'], nodes)).toEqual({ kept: [], dropped: 2 })
  })
})

describe('isStaleTargetRefusal', () => {
  const refused = (status: number, message: string) => ({
    response: { status, data: { message } },
  })

  it('reads the start refused over a target that is not a node', () => {
    expect(isStaleTargetRefusal(refused(404, 'node not found'))).toBe(true)
  })

  it('does not read a missing playbook or another refusal as one', () => {
    expect(isStaleTargetRefusal(refused(404, 'template not found'))).toBe(false)
    expect(isStaleTargetRefusal(refused(403, 'node not found'))).toBe(false)
    expect(isStaleTargetRefusal(new Error('Network Error'))).toBe(false)
  })
})
