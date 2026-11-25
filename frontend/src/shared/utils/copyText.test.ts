import { describe, expect, it, vi } from 'vitest'

import { copyText, type CopyEnvironment } from './copyText'

const env = (over: Partial<CopyEnvironment>): CopyEnvironment => ({
  isSecureContext: true,
  clipboard: { writeText: vi.fn().mockResolvedValue(undefined) },
  execCopy: vi.fn().mockReturnValue(true),
  ...over,
})

describe('copyText', () => {
  it('uses the clipboard API in a secure context', async () => {
    const e = env({})
    expect(await copyText('x', e)).toBe(true)
    expect(e.clipboard?.writeText).toHaveBeenCalledWith('x')
    expect(e.execCopy).not.toHaveBeenCalled()
  })

  it('falls back to a selection copy where the clipboard API is absent', async () => {
    const e = env({ isSecureContext: false, clipboard: undefined })
    expect(await copyText('x', e)).toBe(true)
    expect(e.execCopy).toHaveBeenCalledWith('x')
  })

  it('falls back when the clipboard API refuses', async () => {
    const e = env({
      clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
    })
    expect(await copyText('x', e)).toBe(true)
    expect(e.execCopy).toHaveBeenCalledWith('x')
  })

  it('reports failure instead of throwing when both paths fail', async () => {
    const e = env({
      isSecureContext: false,
      clipboard: undefined,
      execCopy: vi.fn(() => {
        throw new Error('no document')
      }),
    })
    await expect(copyText('x', e)).resolves.toBe(false)
  })

  it('reports failure when the selection copy is refused', async () => {
    const e = env({
      isSecureContext: false,
      clipboard: undefined,
      execCopy: vi.fn().mockReturnValue(false),
    })
    expect(await copyText('x', e)).toBe(false)
  })
})
