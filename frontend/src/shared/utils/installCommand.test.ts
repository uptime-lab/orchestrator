import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'

import { buildInstallCommand } from './installCommand'

const blob = (platformUrl: string) =>
  btoa(JSON.stringify({ master_token: 'mt_x', platform_url: platformUrl }))

// The tests run without a DOM; the function only reads the page origin.
const origin = 'https://ui.example.com'
beforeAll(() => {
  vi.stubGlobal('window', { location: { origin } })
})
afterAll(() => {
  vi.unstubAllGlobals()
})

describe('buildInstallCommand', () => {
  it('takes the script from the platform URL inside the token', () => {
    const token = blob('https://orchestrator.example.com/')
    expect(buildInstallCommand(token)).toBe(
      `curl -fsSL https://orchestrator.example.com/install.sh | sudo sh -s -- --token '${token}'`,
    )
  })

  it('adds --insecure for an install that advertises plain http', () => {
    const token = blob('http://orchestrator.example.com')
    expect(buildInstallCommand(token)).toBe(
      `curl -fsSL http://orchestrator.example.com/install.sh | sudo sh -s -- --token '${token}' --insecure`,
    )
  })

  it('falls back to the page origin for a token it cannot decode', () => {
    expect(buildInstallCommand('not-base64!')).toBe(
      `curl -fsSL ${origin}/install.sh | sudo sh -s -- --token 'not-base64!'`,
    )
  })
})
