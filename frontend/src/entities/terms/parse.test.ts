import { describe, expect, it } from 'vitest'

import { needsTermsAcceptance, parseTermsUrl, termsAcceptance } from './parse'

describe('parseTermsUrl', () => {
  it('accepts an absolute https URL', () => {
    expect(parseTermsUrl('https://example.com/terms')).toBe(
      'https://example.com/terms',
    )
    expect(parseTermsUrl('  https://example.com/terms  ')).toBe(
      'https://example.com/terms',
    )
  })

  it.each([
    undefined,
    null,
    42,
    '',
    '   ',
    'http://example.com/terms',
    'javascript:alert(1)',
    'data:text/html,hi',
    '/terms',
    'example.com/terms',
    'ftp://example.com/terms',
    'not a url',
  ])('treats %j as unset', (raw) => {
    expect(parseTermsUrl(raw)).toBeNull()
  })
})

describe('termsAcceptance', () => {
  it('asks for nothing when no terms are configured', () => {
    expect(termsAcceptance(null, false)).toEqual({ ok: true })
    // A stray box on a page without terms must not claim an acceptance.
    expect(termsAcceptance(null, true)).toEqual({ ok: true })
  })

  it('requires the box when terms are configured', () => {
    expect(termsAcceptance('https://example.com/terms', false)).toEqual({
      ok: false,
    })
    expect(termsAcceptance('https://example.com/terms', true)).toEqual({
      ok: true,
      terms_accepted: true,
    })
  })
})

describe('needsTermsAcceptance', () => {
  const url = 'https://example.com/terms'

  it('asks a signed-in user with no stored acceptance', () => {
    expect(needsTermsAcceptance(url, { terms_accepted: false })).toBe(true)
  })

  it('does not ask a user whose acceptance is stored', () => {
    expect(needsTermsAcceptance(url, { terms_accepted: true })).toBe(false)
  })

  it('asks nobody on an install with no terms', () => {
    expect(needsTermsAcceptance(null, { terms_accepted: false })).toBe(false)
  })

  it('asks nobody before sign-in', () => {
    expect(needsTermsAcceptance(url, undefined)).toBe(false)
  })
})
