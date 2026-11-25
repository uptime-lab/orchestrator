import { describe, expect, it } from 'vitest'

import { contactHref, isEmailContact } from './contact'

describe('contact', () => {
  it('links an e-mail address and nothing else', () => {
    expect(contactHref('sales@example.com')).toBe('mailto:sales@example.com')
    expect(isEmailContact('Write to sales@example.com')).toBe(false)
    expect(contactHref('https://example.com/contact')).toBe('')
    expect(contactHref('javascript:alert(1)')).toBe('')
    expect(contactHref('two words@example.com')).toBe('')
  })
})
