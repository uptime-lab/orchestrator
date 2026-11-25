import { describe, expect, it } from 'vitest'

import { latestAgentVersion } from './latestAgentVersion'

describe('latestAgentVersion', () => {
  it('compares against what latest installs, not the top of the list', () => {
    expect(
      latestAgentVersion({
        versions: ['latest', '0.8.0', '0.7.9'],
        latest: '0.7.9',
      }),
    ).toBe('0.7.9')
  })

  it('falls back to the newest listed version without the field', () => {
    expect(latestAgentVersion({ versions: ['latest', '0.8.0', '0.7.9'] })).toBe(
      '0.8.0',
    )
  })

  it('never answers with the alias itself', () => {
    expect(latestAgentVersion({ versions: ['latest'], latest: 'latest' })).toBe(
      null,
    )
    expect(latestAgentVersion(null)).toBe(null)
  })
})
