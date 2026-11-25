// The token pair lives in localStorage, which every tab of the origin shares,
// while what a tab SHOWS — its stores, caches and socket — belongs to the
// user and account it loaded with. A tab therefore pins the identity it signed
// in as and compares the stored token against it: a token for anyone else
// means another tab switched account or signed in as someone else, and this
// tab must stop acting until it reloads.

// tokenIdentity names who a token speaks for: the user and the account. Null
// for a missing or unreadable token. A refreshed token keeps the identity.
export const tokenIdentity = (token: string | null): string | null => {
  if (!token) return null
  try {
    const part = token.split('.')[1]
    if (!part) return null
    const base64 = part.replace(/-/g, '+').replace(/_/g, '/')
    const json = decodeURIComponent(
      atob(base64)
        .split('')
        .map((c) => `%${`00${c.charCodeAt(0).toString(16)}`.slice(-2)}`)
        .join(''),
    )
    const claims = JSON.parse(json) as {
      user_id?: unknown
      account_id?: unknown
    }
    if (typeof claims.user_id !== 'string' || !claims.user_id) return null
    const account =
      typeof claims.account_id === 'string' ? claims.account_id : ''
    return `${claims.user_id}/${account}`
  } catch {
    return null
  }
}

export type IdentityChange = 'same' | 'signed-out' | 'switched'

// classifyIdentityChange compares the identity a tab pinned with the token now
// in storage. A tab that never pinned one has nothing to protect.
export const classifyIdentityChange = (
  pinned: string | null,
  storedToken: string | null,
): IdentityChange => {
  if (pinned === null) return 'same'
  if (!storedToken) return 'signed-out'
  const current = tokenIdentity(storedToken)
  if (current === null) return 'signed-out'
  return current === pinned ? 'same' : 'switched'
}
