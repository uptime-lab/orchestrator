import { describe, expect, it } from 'vitest'

import {
  clearsRequiredTitle,
  hasLineBreak,
  isBlank,
  isEmailAddress,
  isEnvKey,
  isSecretName,
} from './formRules'

describe('isBlank', () => {
  it('treats a name of spaces as no name', () => {
    expect(isBlank('')).toBe(true)
    expect(isBlank('   ')).toBe(true)
    expect(isBlank(' node-1 ')).toBe(false)
  })
})

describe('isSecretName', () => {
  it('accepts what a {{secrets.<name>}} reference can name', () => {
    for (const name of [
      'REGISTRY_TOKEN',
      'ci.token',
      'site-a_key',
      'x9',
      'Registry credentials',
      'a/b',
      'ключ',
      'a{b',
    ]) {
      expect(isSecretName(name)).toBe(true)
    }
  })

  it('refuses a name a reference cannot reach', () => {
    for (const name of ['', '   ', 'token}}', 'a}b']) {
      expect(isSecretName(name)).toBe(false)
    }
  })
})

describe('isEnvKey', () => {
  it('accepts a shell identifier', () => {
    for (const key of ['A', '_A', 'a1_B2', 'API_TOKEN']) {
      expect(isEnvKey(key)).toBe(true)
    }
  })

  it('refuses a key the agent would refuse', () => {
    for (const key of ['', '1ABC', 'A-B', 'A B', 'A=B', 'A.B', 'ÉTÉ', ' A']) {
      expect(isEnvKey(key)).toBe(false)
    }
  })
})

describe('hasLineBreak', () => {
  it('finds a line feed or a carriage return', () => {
    expect(hasLineBreak('a\nb')).toBe(true)
    expect(hasLineBreak('a\r\nb')).toBe(true)
    expect(hasLineBreak('a\rb')).toBe(true)
    expect(hasLineBreak('one line with spaces')).toBe(false)
  })
})

describe('isEmailAddress', () => {
  it('accepts one address with a domain dot', () => {
    expect(isEmailAddress('name@example.com')).toBe(true)
    expect(isEmailAddress(' first.last+tag@sub.example.org ')).toBe(true)
  })

  it('refuses what cannot receive the invitation', () => {
    for (const value of [
      '',
      'name',
      'name@localhost',
      'name@@example.com',
      'two words@example.com',
      'Name <name@example.com>',
      'a@example.com,b@example.com',
    ]) {
      expect(isEmailAddress(value)).toBe(false)
    }
  })
})

describe('clearsRequiredTitle', () => {
  it('refuses blanking a title the record has', () => {
    expect(clearsRequiredTitle('', 'Ubuntu 24.04')).toBe(true)
    expect(clearsRequiredTitle('   ', 'Ubuntu 24.04')).toBe(true)
  })

  it('keeps a renamed title and a record stored without one', () => {
    expect(clearsRequiredTitle('Ubuntu 24.10', 'Ubuntu 24.04')).toBe(false)
    expect(clearsRequiredTitle('', '')).toBe(false)
  })
})
