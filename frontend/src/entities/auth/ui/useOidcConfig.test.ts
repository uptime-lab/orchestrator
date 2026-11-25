import { beforeEach, describe, expect, it, vi } from 'vitest'

const getSignInOptions = vi.fn()
const getOidcConfig = vi.fn()

vi.mock('@/entities/signInOptions/api', () => ({
  getSignInOptions: () => getSignInOptions(),
}))
vi.mock('../api/oidc.ts', () => ({
  getOidcConfig: () => getOidcConfig(),
}))

const { forgetSignInOptions } = await import('@/entities/signInOptions')
const { loadOidcConfig } = await import('./useOidcConfig')

beforeEach(() => {
  getSignInOptions.mockReset()
  getOidcConfig.mockReset()
  forgetSignInOptions()
})

// An install without single sign-on serves no /auth/oidc/config, so every
// ask is a 404 in its log; the sign-in options say whether to ask at all.
// The provider answer is kept once found, so the asking cases run last.
describe('the provider is asked for only where single sign-on is served', () => {
  it('does not ask when the sign-in options say single sign-on is off', async () => {
    getSignInOptions.mockResolvedValue({
      email_delivery: false,
      registration: 'closed',
      single_sign_on: false,
    })
    expect(await loadOidcConfig()).toBeNull()
    expect(await loadOidcConfig()).toBeNull()
    expect(getOidcConfig).not.toHaveBeenCalled()
  })

  it('asks when the sign-in options cannot be read', async () => {
    getSignInOptions.mockResolvedValue(null)
    getOidcConfig.mockResolvedValue(null)
    expect(await loadOidcConfig()).toBeNull()
    expect(getOidcConfig).toHaveBeenCalledTimes(1)
  })

  it('asks when an older backend does not report single sign-on', async () => {
    getSignInOptions.mockResolvedValue({ email_delivery: false })
    getOidcConfig.mockResolvedValue({ enabled: true, label: 'x' })
    expect((await loadOidcConfig())?.enabled).toBe(true)
    expect(getOidcConfig).toHaveBeenCalledTimes(1)
  })
})
