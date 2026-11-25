import { describe, expect, it } from 'vitest'

import { agentUpdateRefusal } from './agentUpdateRefusal'

const refused = (status: number, data: Record<string, string>) => ({
  response: { status, data },
})

describe('agentUpdateRefusal', () => {
  it('says every refusal the command route gives before a command exists', () => {
    for (const err of [
      refused(409, { code: 'overlay_root', message: 'overlay root' }),
      refused(409, { error: 'node_not_approved', message: 'not approved' }),
      refused(409, { error: 'node_unavailable', message: 'offline' }),
      refused(429, { error: 'too many requests' }),
      refused(503, {
        error: 'service_unavailable',
        message: 'node is not connected',
      }),
      new Error('Network Error'),
    ]) {
      expect(agentUpdateRefusal(err)).toBe('refused')
    }
  })

  it('treats a node already on the version as information', () => {
    expect(
      agentUpdateRefusal(
        refused(409, { error: 'agent_already_current', message: 'current' }),
      ),
    ).toBe('already-current')
    expect(
      agentUpdateRefusal(refused(409, { code: 'agent_already_current' })),
    ).toBe('already-current')
  })

  it('leaves a 403 to the permission toast', () => {
    expect(agentUpdateRefusal(refused(403, { message: 'forbidden' }))).toBe(
      'reported',
    )
  })
})
