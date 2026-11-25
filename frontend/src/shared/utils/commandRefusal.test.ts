import { describe, expect, it } from 'vitest'

import '@/i18n'

import { extractApiErrorMessage } from './apiError'
import { commandRefusalText } from './commandRefusal'

// The node-state refusals name the next step instead of the server's sentence,
// which carries ids, statuses and request fields.
describe('commandRefusalText', () => {
  it('reads the code from the generic command route body', () => {
    const err = {
      response: {
        data: {
          error: 'node_unavailable',
          message: 'node is not available (current status: offline)',
        },
      },
    }
    expect(commandRefusalText(err)).toBe(
      'The node is offline, so this command cannot be sent. Bring the node online and try again.',
    )
    expect(extractApiErrorMessage(err, 'fb')).toBe(commandRefusalText(err))
  })

  it('reads the code from the standard envelope', () => {
    const err = {
      response: {
        data: { code: 'recovery_unreachable', message: 'node x cannot' },
      },
    }
    expect(commandRefusalText(err)).toMatch(/cannot be sent to recovery/)
  })

  it.each([
    ['node_not_approved', 'Approve this node before sending it commands.'],
    ['agent_already_current', 'This node already runs that agent version.'],
    [
      'node_not_connected',
      "The node's connection just dropped. Try again in a moment.",
    ],
  ])('words %s', (code, text) => {
    expect(commandRefusalText({ response: { data: { code } } })).toBe(text)
  })

  it("words the generic route's not-connected answer by its message", () => {
    expect(
      commandRefusalText({
        response: {
          data: {
            error: 'service_unavailable',
            message: 'node is not connected',
          },
        },
      }),
    ).toBe("The node's connection just dropped. Try again in a moment.")
    expect(
      commandRefusalText({
        response: { data: { error: 'service_unavailable', message: 'other' } },
      }),
    ).toBeUndefined()
  })

  it('leaves every other answer to the server message', () => {
    const err = { response: { data: { error: 'invalid token' } } }
    expect(commandRefusalText(err)).toBeUndefined()
    expect(extractApiErrorMessage(err, 'fb')).toBe('invalid token')
  })
})
