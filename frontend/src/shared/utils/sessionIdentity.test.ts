import { describe, expect, it } from 'vitest'

import { classifyIdentityChange, tokenIdentity } from './sessionIdentity'

const b64url = (s: string) =>
  btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')

const jwt = (claims: Record<string, unknown>) =>
  `${b64url('{"alg":"HS256"}')}.${b64url(JSON.stringify(claims))}.sig`

const userA = 'user-a'
const accountOne = 'account-1'
const accountTwo = 'account-2'

describe('tokenIdentity', () => {
  it('names the user and the account', () => {
    expect(
      tokenIdentity(jwt({ user_id: userA, account_id: accountOne, exp: 1 })),
    ).toBe(`${userA}/${accountOne}`)
  })

  it('is unchanged by a refresh', () => {
    const before = jwt({ user_id: userA, account_id: accountOne, exp: 1 })
    const after = jwt({ user_id: userA, account_id: accountOne, exp: 2 })
    expect(tokenIdentity(after)).toBe(tokenIdentity(before))
  })

  it('is null for a missing or unreadable token', () => {
    expect(tokenIdentity(null)).toBeNull()
    expect(tokenIdentity('not-a-jwt')).toBeNull()
    expect(tokenIdentity('a.!!!.c')).toBeNull()
  })
})

describe('classifyIdentityChange', () => {
  const pinned = `${userA}/${accountOne}`

  it('sees a refreshed token as the same session', () => {
    expect(
      classifyIdentityChange(
        pinned,
        jwt({ user_id: userA, account_id: accountOne, exp: 99 }),
      ),
    ).toBe('same')
  })

  it('sees another account as a switch', () => {
    expect(
      classifyIdentityChange(
        pinned,
        jwt({ user_id: userA, account_id: accountTwo }),
      ),
    ).toBe('switched')
  })

  it('sees another user as a switch', () => {
    expect(
      classifyIdentityChange(
        pinned,
        jwt({ user_id: 'user-b', account_id: accountOne }),
      ),
    ).toBe('switched')
  })

  it('sees a cleared token as a sign-out', () => {
    expect(classifyIdentityChange(pinned, null)).toBe('signed-out')
  })

  it('ignores changes in a tab that never signed in', () => {
    expect(
      classifyIdentityChange(
        null,
        jwt({ user_id: userA, account_id: accountTwo }),
      ),
    ).toBe('same')
  })
})
