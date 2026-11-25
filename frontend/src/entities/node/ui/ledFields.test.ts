import { describe, expect, it } from 'vitest'

import { ledRowToSpec } from './ledFields'

describe('ledRowToSpec', () => {
  // Brightness is an integer on the server; 2.5 refused the whole save.
  it('sends a whole brightness', () => {
    expect(
      ledRowToSpec({ source: 'load', low: '', high: '', brightness: '2.5' })
        .brightness,
    ).toBe(3)
  })
})
