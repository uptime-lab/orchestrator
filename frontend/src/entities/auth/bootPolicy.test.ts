import { describe, expect, it } from 'vitest'

import { RefreshTokenMissingError } from '@/shared/utils/refreshPolicy'

import {
  BOOT_RETRY_DELAYS_MS,
  bootFailureAction,
  bootRetryDelay,
  NO_TOKENS_MESSAGE,
} from './bootPolicy'

const axiosFailure = (status?: number, code = 'ERR_BAD_RESPONSE') =>
  Object.assign(new Error('failed'), {
    isAxiosError: true,
    code,
    response: status ? { status } : undefined,
  })

describe('bootFailureAction', () => {
  it.each([408, 429, 500, 502, 503, 504])('retries on %i', (status) => {
    expect(bootFailureAction(axiosFailure(status))).toBe('retry')
  })

  it.each(['ERR_NETWORK', 'ECONNABORTED', 'ETIMEDOUT'])(
    'retries on a request with no answer (%s)',
    (code) => {
      expect(bootFailureAction(axiosFailure(undefined, code))).toBe('retry')
    },
  )

  it.each([400, 401, 403, 404])('ends the session on %i', (status) => {
    expect(bootFailureAction(axiosFailure(status))).toBe('sign-out')
  })

  it('ends the session when there are no tokens', () => {
    expect(bootFailureAction(new Error(NO_TOKENS_MESSAGE))).toBe('sign-out')
    expect(bootFailureAction(new RefreshTokenMissingError())).toBe('sign-out')
  })

  it('does nothing for a cancelled request', () => {
    expect(bootFailureAction(axiosFailure(undefined, 'ERR_CANCELED'))).toBe(
      'ignore',
    )
  })

  it('does not retry an error that is not a request failure', () => {
    expect(bootFailureAction(new TypeError('bad shape'))).toBe('sign-out')
  })
})

describe('bootRetryDelay', () => {
  it('grows and then holds at the last pause', () => {
    expect(bootRetryDelay(0)).toBe(BOOT_RETRY_DELAYS_MS[0])
    expect(bootRetryDelay(1)).toBeGreaterThan(bootRetryDelay(0))
    expect(bootRetryDelay(100)).toBe(
      BOOT_RETRY_DELAYS_MS[BOOT_RETRY_DELAYS_MS.length - 1],
    )
  })
})
