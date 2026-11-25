import { describe, expect, it } from 'vitest'

import {
  initialTerminalView,
  sessionWanted,
  terminalViewReducer,
} from './terminalView'

const opened = terminalViewReducer(initialTerminalView(false), {
  type: 'open',
})

describe('terminalViewReducer', () => {
  it('opens the embedded view at once and the row view on demand', () => {
    expect(sessionWanted(initialTerminalView(true))).toBe(true)
    expect(sessionWanted(initialTerminalView(false))).toBe(false)
    expect(sessionWanted(opened)).toBe(true)
  })

  it('keeps the view open with the message when the session is refused', () => {
    const refused = terminalViewReducer(opened, {
      type: 'refuse',
      message: 'Access revoked',
    })
    expect(refused.open).toBe(true)
    expect(refused.error).toBe('Access revoked')
    expect(sessionWanted(refused)).toBe(false)
  })

  it('starts a fresh session on retry', () => {
    const refused = terminalViewReducer(opened, {
      type: 'refuse',
      message: 'No response',
    })
    const retried = terminalViewReducer(refused, { type: 'retry' })
    expect(retried.error).toBeNull()
    expect(sessionWanted(retried)).toBe(true)
    expect(retried.attempt).toBe(refused.attempt + 1)
  })

  it('leaves the view only on an explicit close', () => {
    const refused = terminalViewReducer(opened, {
      type: 'refuse',
      message: 'No response',
    })
    const closed = terminalViewReducer(refused, { type: 'close' })
    expect(closed.open).toBe(false)
    expect(closed.error).toBeNull()
    expect(sessionWanted(closed)).toBe(false)
  })

  it('ignores a late refusal after the view was closed', () => {
    const closed = terminalViewReducer(opened, { type: 'close' })
    expect(terminalViewReducer(closed, { type: 'refuse', message: 'x' })).toBe(
      closed,
    )
  })
})
