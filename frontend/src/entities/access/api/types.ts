// Declarative shell access: an entry is a named OS user with a set of SSH keys,
// or a trusted external CA. The password value is never returned by the API —
// only whether one is set.

export type AccessEntryType = 'user' | 'ca'

// 'unassigned' = the entry has no target and is not account-wide, so it applies
// to no node (the safe default for a freshly created entry). 'account' = the
// fleet-wide default. 'ou'/'node' = attached to a specific target.
export type AccessScope = 'account' | 'ou' | 'node' | 'unassigned'

// Lifecycle is DERIVED from the key set by the server, never computed here.
// An empty key set means the OS user is locked (account expired, home kept);
// adding a key back unlocks it. Deriving it in the UI would drift from what the
// agent actually does.
export type AccessLifecycle = 'active' | 'locked'

export type AccessEntry = {
  id: string
  account_id: string
  type: AccessEntryType
  scope: AccessScope
  // Units the entry lives in — a user can be in several OUs at once.
  ou_ids: string[]
  node_id?: string
  account_wide: boolean
  username?: string
  sudo: boolean
  has_password: boolean
  adopt_unmanaged: boolean
  principals?: string[]
  key_ids: string[]
  lifecycle?: AccessLifecycle
  created_at: string
  updated_at: string
}

// Where an effective entry came from, so the UI can attribute every key and
// explain why one entry shadows another.
export type AccessSource = {
  level: 'node' | 'ou' | 'root' | 'account'
  ou_id?: string
  entry_id: string
}

export type EffectiveAccessEntry = {
  entry_id: string
  type: AccessEntryType
  username?: string
  sudo: boolean
  has_password: boolean
  principals?: string[]
  key_ids: string[]
  lifecycle?: AccessLifecycle
  source: AccessSource
  // Lower-precedence entries for the same username that this one replaced.
  // Rendered explicitly: an entry that exists but has no effect is the same
  // class of bug as the silent key replacement this feature removes.
  overridden?: AccessSource[]
}

// One rail bucket's counts split by entry type, so the rail can show the
// number matching the active tab (Users vs CA Certificates).
export type AccessBucketCounts = {
  users: number
  cas: number
}

// Per-OU entry counts for the credentials filter rail.
export type AccessOUCount = {
  ou_id: string
  count: AccessBucketCounts
}

export type AccessOUCountsResult = {
  ous: AccessOUCount[]
  // Entries with no target and not account-wide (apply to no node).
  unassigned: AccessBucketCounts
  // Account-wide default entries (apply fleet-wide).
  account_wide: AccessBucketCounts
  total: number
}

export type CreateAccessEntryRequest = {
  type: AccessEntryType
  // Units to scope the entry to (a user can live in several). The legacy
  // single ou_id is still accepted by the backend and merged into the set.
  ou_ids?: string[]
  ou_id?: string
  node_id?: string
  // Only meaningful with no units and no node: true = fleet-wide default,
  // false/omitted = unassigned (applies to no node).
  account_wide?: boolean
  username?: string
  sudo?: boolean
  password?: string
  ca_key_id?: string
  principals?: string[]
  key_ids?: string[]
}

// Nil fields are left unchanged. clear_password is separate from password
// because an empty string cannot distinguish "leave it" from "remove it".
export type UpdateAccessEntryRequest = {
  username?: string
  sudo?: boolean
  password?: string
  clear_password?: boolean
  principals?: string[]
  // A non-nil scope re-targets the entry. Omit to leave the scope unchanged.
  // No units and no node = unassigned unless account_wide is true.
  scope?: {
    ou_ids?: string[]
    ou_id?: string
    node_id?: string
    account_wide?: boolean
  }
}
