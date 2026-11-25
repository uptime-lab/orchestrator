import { describe, expect, it } from 'vitest'

import { MAX_INPUT_FRAME_BYTES, splitInputFrames } from './terminalInput'

const encodedBytes = (s: string) =>
  new TextEncoder().encode(JSON.stringify(s)).length - 2

describe('splitInputFrames', () => {
  it('leaves a keystroke alone', () => {
    expect(splitInputFrames('a')).toEqual(['a'])
    expect(splitInputFrames('\x1b[A')).toEqual(['\x1b[A'])
  })

  it('returns nothing for an empty input', () => {
    expect(splitInputFrames('')).toEqual([])
  })

  it('splits a large paste into frames under the bound and loses nothing', () => {
    const paste = 'x'.repeat(100 * 1024)
    const frames = splitInputFrames(paste)
    expect(frames.length).toBeGreaterThan(1)
    expect(frames.join('')).toBe(paste)
    for (const frame of frames) {
      expect(encodedBytes(frame)).toBeLessThanOrEqual(MAX_INPUT_FRAME_BYTES)
    }
  })

  it('bounds the ENCODED size, so control characters and quotes do not overflow', () => {
    const hostile = '"\\\n\t\x01'.repeat(8 * 1024)
    for (const frame of splitInputFrames(hostile)) {
      expect(encodedBytes(frame)).toBeLessThanOrEqual(MAX_INPUT_FRAME_BYTES)
    }
  })

  it('never cuts a surrogate pair in half', () => {
    const emoji = '\u{1F600}'
    const paste = emoji.repeat(10_000)
    const frames = splitInputFrames(paste, 1000)
    expect(frames.join('')).toBe(paste)
    for (const frame of frames) {
      expect(frame.length % 2).toBe(0)
      expect(encodedBytes(frame)).toBeLessThanOrEqual(1000)
    }
  })

  it('counts multi-byte characters by their UTF-8 length', () => {
    const paste = 'ж'.repeat(3000) // 2 bytes each
    const frames = splitInputFrames(paste, 1000)
    expect(frames.join('')).toBe(paste)
    for (const frame of frames) {
      expect(encodedBytes(frame)).toBeLessThanOrEqual(1000)
    }
  })
})
