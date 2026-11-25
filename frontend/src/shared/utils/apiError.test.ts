import { describe, expect, it } from 'vitest'

import {
  extractApiErrorMessage,
  isReauthCancelled,
  markReauthCancelled,
} from './apiError'

// The extraction order is load-bearing: the server's own message beats the
// legacy error field beats axios's transport message beats the fallback —
// each shadow below is a real response shape the API emits.
describe('extractApiErrorMessage', () => {
  it('prefers the response message', () => {
    const err = {
      response: { data: { message: 'node not found', error: 'legacy' } },
      message: 'Request failed with status code 404',
    }
    expect(extractApiErrorMessage(err, 'fallback')).toBe('node not found')
  })

  it('falls back to the legacy error field', () => {
    const err = { response: { data: { error: 'invalid token' } } }
    expect(extractApiErrorMessage(err, 'fallback')).toBe('invalid token')
  })

  it('falls back to the transport message', () => {
    expect(extractApiErrorMessage(new Error('Network Error'), 'fallback')).toBe(
      'Network Error',
    )
  })

  it('uses the fallback for shapes it does not know', () => {
    expect(extractApiErrorMessage(undefined, 'fallback')).toBe('fallback')
    expect(
      extractApiErrorMessage({ response: { data: { message: 42 } } }, 'fb'),
    ).toBe('fb')
    expect(extractApiErrorMessage('a bare string', 'fb')).toBe('fb')
  })
})

describe('a cancelled identity prompt', () => {
  const refused = () => ({
    response: {
      status: 403,
      data: {
        message: 'confirm your identity to continue',
        code: 'reauth_required',
      },
    },
  })

  it('is reported as nothing once marked', () => {
    const err = refused()
    markReauthCancelled(err)
    expect(isReauthCancelled(err)).toBe(true)
    expect(extractApiErrorMessage(err, 'fallback')).toBe('')
  })

  it('leaves an unmarked refusal its message', () => {
    const err = refused()
    expect(isReauthCancelled(err)).toBe(false)
    expect(extractApiErrorMessage(err, 'fallback')).toBe(
      'confirm your identity to continue',
    )
  })
})
