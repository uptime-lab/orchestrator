import { beforeEach, describe, expect, it, vi } from 'vitest'

const getSignInOptions = vi.fn()
const getOidcConfig = vi.fn()

vi.mock('./api', () => ({
  getSignInOptions: () => getSignInOptions(),
}))
vi.mock('@/entities/auth/api/oidc.ts', () => ({
  getOidcConfig: () => getOidcConfig(),
}))

const { forgetSignInOptions, loadSignInOptions } = await import('./hooks')
const { loadOidcConfig } = await import('@/entities/auth/ui/useOidcConfig')

beforeEach(() => {
  getSignInOptions.mockReset()
  getOidcConfig.mockReset()
  // The provider lookup reads the sign-in options first; start every case
  // from a page that has not asked yet.
  forgetSignInOptions()
})

describe('the install signals are fetched once per page', () => {
  it('asks /auth/oidc/config once for every component', async () => {
    getSignInOptions.mockResolvedValue({
      email_delivery: false,
      single_sign_on: true,
    })
    getOidcConfig.mockResolvedValue({ enabled: true, label: 'x' })
    await Promise.all([loadOidcConfig(), loadOidcConfig()])
    await loadOidcConfig()
    expect(getOidcConfig).toHaveBeenCalledTimes(1)
  })

  it('asks the sign-in options again once the sign-up route forgets them', async () => {
    getSignInOptions
      .mockResolvedValueOnce({
        email_delivery: false,
        registration: 'first_owner',
      })
      .mockResolvedValueOnce({
        email_delivery: false,
        registration: 'invite_code',
      })
    expect((await loadSignInOptions())?.registration).toBe('first_owner')
    expect((await loadSignInOptions())?.registration).toBe('first_owner')
    forgetSignInOptions()
    expect((await loadSignInOptions())?.registration).toBe('invite_code')
    expect(getSignInOptions).toHaveBeenCalledTimes(2)
  })
})
