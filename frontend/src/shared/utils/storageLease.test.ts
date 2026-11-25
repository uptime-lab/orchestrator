import { describe, expect, it } from 'vitest'

import { withStorageLease, type LeaseStorage } from './storageLease'

const memoryStorage = (): LeaseStorage & { data: Map<string, string> } => {
  const data = new Map<string, string>()
  return {
    data,
    getItem: (k) => data.get(k) ?? null,
    setItem: (k, v) => {
      data.set(k, v)
    },
    removeItem: (k) => {
      data.delete(k)
    },
  }
}

const opts = (storage: LeaseStorage, ownerId: string) => ({
  storage,
  key: 'lease',
  ownerId,
  ttlMs: 5_000,
  maxWaitMs: 2_000,
  settleMs: 5,
  pollMs: 5,
})

const tick = (ms: number) =>
  new Promise<void>((resolve) => {
    setTimeout(resolve, ms)
  })

describe('withStorageLease', () => {
  it('never runs two holders at once', async () => {
    const storage = memoryStorage()
    let running = 0
    let maxRunning = 0
    const work = async () => {
      running++
      maxRunning = Math.max(maxRunning, running)
      await tick(20)
      running--
    }
    await Promise.all([
      withStorageLease(opts(storage, 'a'), work),
      withStorageLease(opts(storage, 'b'), work),
      withStorageLease(opts(storage, 'c'), work),
    ])
    expect(maxRunning).toBe(1)
  })

  it('releases the lease when done', async () => {
    const storage = memoryStorage()
    await withStorageLease(opts(storage, 'a'), async () => {
      expect(storage.data.has('lease')).toBe(true)
    })
    expect(storage.data.has('lease')).toBe(false)
  })

  it('releases the lease when the work throws', async () => {
    const storage = memoryStorage()
    await expect(
      withStorageLease(opts(storage, 'a'), async () => {
        throw new Error('boom')
      }),
    ).rejects.toThrow('boom')
    expect(storage.data.has('lease')).toBe(false)
  })

  it('takes over an expired lease', async () => {
    const storage = memoryStorage()
    storage.setItem(
      'lease',
      JSON.stringify({ owner: 'gone', expires: Date.now() - 1 }),
    )
    const started = Date.now()
    await withStorageLease(opts(storage, 'a'), async () => {
      expect(JSON.parse(storage.getItem('lease') ?? '{}').owner).toBe('a')
    })
    expect(Date.now() - started).toBeLessThan(500)
  })

  it('runs anyway once the wait limit passes', async () => {
    const storage = memoryStorage()
    storage.setItem(
      'lease',
      JSON.stringify({ owner: 'stuck', expires: Date.now() + 60_000 }),
    )
    let ran = false
    await withStorageLease(
      { ...opts(storage, 'a'), maxWaitMs: 30 },
      async () => {
        ran = true
      },
    )
    expect(ran).toBe(true)
    // Someone else's lease is not ours to remove.
    expect(JSON.parse(storage.getItem('lease') ?? '{}').owner).toBe('stuck')
  })
})
