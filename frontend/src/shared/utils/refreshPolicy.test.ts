import { describe, expect, it, vi } from 'vitest'

import {
  isDefinitiveRefreshRefusal,
  RefreshTokenMissingError,
  retryTransient,
} from './refreshPolicy'

const httpError = (status: number) =>
  Object.assign(new Error(`status ${status}`), { response: { status } })

describe('isDefinitiveRefreshRefusal', () => {
  it.each([400, 401, 403])('ends the session on %i', (status) => {
    expect(isDefinitiveRefreshRefusal(httpError(status))).toBe(true)
  })

  it('ends the session when there is no refresh token', () => {
    expect(isDefinitiveRefreshRefusal(new RefreshTokenMissingError())).toBe(
      true,
    )
  })

  it.each([429, 500, 502, 503, 504])('keeps the session on %i', (status) => {
    expect(isDefinitiveRefreshRefusal(httpError(status))).toBe(false)
  })

  it('keeps the session on a network error with no response', () => {
    const networkError = Object.assign(new Error('Network Error'), {
      code: 'ERR_NETWORK',
    })
    expect(isDefinitiveRefreshRefusal(networkError)).toBe(false)
    expect(isDefinitiveRefreshRefusal(undefined)).toBe(false)
  })
})

describe('retryTransient', () => {
  const noSleep = vi.fn().mockResolvedValue(undefined)

  it('retries a transient failure and returns the later success', async () => {
    const fn = vi
      .fn()
      .mockRejectedValueOnce(httpError(429))
      .mockRejectedValueOnce(httpError(503))
      .mockResolvedValueOnce('token')
    await expect(retryTransient(fn, [1, 2], noSleep)).resolves.toBe('token')
    expect(fn).toHaveBeenCalledTimes(3)
  })

  it('backs off with growing delays', async () => {
    const sleep = vi.fn().mockResolvedValue(undefined)
    const fn = vi.fn().mockRejectedValue(httpError(500))
    await expect(retryTransient(fn, [10, 30], sleep)).rejects.toThrow()
    expect(sleep.mock.calls.map((c) => c[0])).toEqual([10, 30])
    expect(fn).toHaveBeenCalledTimes(3)
  })

  it('does not retry a definitive refusal', async () => {
    const fn = vi.fn().mockRejectedValue(httpError(401))
    await expect(retryTransient(fn, [1, 2], noSleep)).rejects.toThrow(
      'status 401',
    )
    expect(fn).toHaveBeenCalledTimes(1)
  })
})
