import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// The config module touches `window` when it loads, and these tests run in a
// node environment; give it an empty one before importing the component.
beforeEach(() => {
  vi.stubGlobal('window', {})
  vi.resetModules()
})

afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const render = async (termsUrl: string | undefined) => {
  if (termsUrl !== undefined) {
    vi.stubEnv('VITE_TERMS_URL', termsUrl)
  }
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
  const { TermsAgreement } = await import('./TermsAgreement')
  return renderToStaticMarkup(<TermsAgreement />)
}

describe('TermsAgreement', () => {
  it('renders a required checkbox linking to the terms when set', async () => {
    const html = await render('https://example.com/terms')
    expect(html).toMatch(/<input[^>]*type="checkbox"[^>]*name="terms"/)
    expect(html).toMatch(/<input[^>]*required/)
    expect(html).toContain('href="https://example.com/terms"')
    expect(html).toContain('target="_blank"')
    expect(html).toContain('rel="noopener noreferrer"')
  })

  it('renders nothing when unset', async () => {
    expect(await render(undefined)).toBe('')
    expect(await render('')).toBe('')
  })

  it('renders nothing for a value that is not https', async () => {
    expect(await render('http://example.com/terms')).toBe('')
    expect(await render('javascript:alert(1)')).toBe('')
  })
})
