import { describe, expect, it, vi } from 'vitest'

import { onceCache } from './onceCache'

describe('onceCache', () => {
  it('asks once for every caller on the page', async () => {
    const fetch = vi.fn().mockResolvedValue({ v: 1 })
    const c = onceCache(fetch)
    const [a, b] = await Promise.all([c.load(), c.load()])
    await c.load()
    expect(a).toEqual({ v: 1 })
    expect(b).toBe(a)
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('does not keep a failed read', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce({ v: 2 })
    const c = onceCache(fetch)
    expect(await c.load()).toBeNull()
    expect(await c.load()).toEqual({ v: 2 })
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('asks again after forget', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce({ v: 1 })
      .mockResolvedValueOnce({ v: 2 })
    const c = onceCache(fetch)
    expect(await c.load()).toEqual({ v: 1 })
    c.forget()
    expect(await c.load()).toEqual({ v: 2 })
  })
})
