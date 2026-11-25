import { describe, expect, it, vi } from 'vitest'

import { writeFromCurrent } from './writeFromCurrent'

describe('writeFromCurrent', () => {
  it('builds the request from what the server holds now', async () => {
    const write = vi.fn(async (r: string[]) => r)
    const result = await writeFromCurrent(
      async () => ['viewer'],
      (current) => [...current, 'manager@unit'],
      write,
    )
    expect(write).toHaveBeenCalledWith(['viewer', 'manager@unit'])
    expect(result).toEqual(['viewer', 'manager@unit'])
  })

  it('skips the write when there is nothing to change', async () => {
    const write = vi.fn(async () => 'written')
    expect(
      await writeFromCurrent(
        async () => 1,
        () => null,
        write,
      ),
    ).toBeNull()
    expect(write).not.toHaveBeenCalled()
  })

  it('writes nothing when the read fails', async () => {
    const write = vi.fn(async () => 'written')
    await expect(
      writeFromCurrent(
        async () => {
          throw new Error('offline')
        },
        () => 'x',
        write,
      ),
    ).rejects.toThrow('offline')
    expect(write).not.toHaveBeenCalled()
  })
})
