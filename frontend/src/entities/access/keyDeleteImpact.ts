import type { AccessEntry } from './api/types'

// What deleting a key does to device login users: every user holding it loses
// it, and a user left with no key and no password is locked on every node it
// reaches (the server's lifecycle rule).
export const keyDeleteImpact = (
  entries: AccessEntry[],
  keyId: string,
): { users: number; locked: number } => {
  const holders = entries.filter(
    (e) => e.type !== 'ca' && e.key_ids.includes(keyId),
  )
  return {
    users: holders.length,
    locked: holders.filter((e) => e.key_ids.length === 1 && !e.has_password)
      .length,
  }
}
