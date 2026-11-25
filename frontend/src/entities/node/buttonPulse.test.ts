import { describe, expect, it } from 'vitest'

import {
  $buttonPressedNodes,
  $dismissedMarks,
  buttonMarkCleared,
  buttonMarkHydrated,
  nodeButtonPressed,
} from './buttonPulse'

describe('a dismissed mark stays dismissed across a reconnect', () => {
  it('remembers which press was thrown away', () => {
    const nodeId = 'n1'
    const pressedAt = '2026-08-20T10:00:00Z'
    const ms = new Date(pressedAt).getTime()

    nodeButtonPressed({
      node_id: nodeId,
      node_name: 'n',
      press_kind: 'short',
      pressed_at: pressedAt,
    })
    expect($buttonPressedNodes.getState()[nodeId]).toBe(ms)

    buttonMarkCleared(nodeId)
    expect($buttonPressedNodes.getState()[nodeId]).toBeUndefined()
    expect($dismissedMarks.getState()[nodeId]).toBe(ms)

    // The registry replays it on the next socket connect.
    buttonMarkHydrated({ nodeId, pressedAt: ms })
    expect($buttonPressedNodes.getState()[nodeId]).toBeUndefined()

    // A NEW press must still show.
    buttonMarkHydrated({ nodeId, pressedAt: ms + 1 })
    expect($buttonPressedNodes.getState()[nodeId]).toBe(ms + 1)
  })
})
