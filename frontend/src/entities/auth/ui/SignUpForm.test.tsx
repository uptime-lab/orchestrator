import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { SignInOptions } from '@/entities/signInOptions'

// The auth model wires browser listeners when it loads, and these tests run
// in a node environment; the form only needs its submit effect to exist.
vi.mock('../model.ts', () => ({ signUpAndLoginAuthFx: vi.fn() }))

// The terms config touches `window` when it loads; give it an empty one.
beforeEach(() => {
  vi.stubGlobal('window', {})
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// The form as the sign-up page renders it for a registration state: the
// invite field follows signUpOutlook, which follows the backend's answer.
const renderFor = async (registration: SignInOptions['registration']) => {
  await import('@/i18n')
  const { signUpOutlook } = await import('../installCopy.ts')
  const { SignUpForm } = await import('./SignUpForm.tsx')
  const outlook = signUpOutlook({ email_delivery: true, registration }, null)
  return renderToStaticMarkup(<SignUpForm inviteField={outlook.inviteField} />)
}

describe('SignUpForm and the registration state', () => {
  it('asks for no invite code where registration is open', async () => {
    const html = await renderFor('open')
    expect(html).toMatch(/<input[^>]*name="email"/)
    expect(html).not.toContain('name="invite-code"')
  })

  it('still asks for the code on an invite-code install', async () => {
    expect(await renderFor('invite_code')).toContain('name="invite-code"')
  })

  // An open sign-up opens no session: the form gives way to "check your
  // email", worded for a fresh address and a registered one alike.
  it('shows the check-your-email answer instead of the form', async () => {
    await import('@/i18n')
    const { SignUpForm } = await import('./SignUpForm.tsx')
    const html = renderToStaticMarkup(<SignUpForm initiallyCheckEmail />)
    expect(html).toContain('signup-check-email')
    expect(html).toContain('Check your email')
    expect(html).not.toContain('name="password"')
  })

  it('asks for no code from the first owner', async () => {
    expect(await renderFor('first_owner')).not.toContain('name="invite-code"')
  })
})
