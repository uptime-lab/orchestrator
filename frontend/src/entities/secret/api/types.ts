// Secret kind. Semantics:
// - env: key = ENV var name, value = its value
// - credential: key = username, value = password
// - opaque: key = optional label, value = the secret
export type SecretKind = 'env' | 'credential' | 'opaque'

export type Secret = {
  id: string
  account_id: string
  name: string
  kind: SecretKind
  key: string
  // Whether a value is set. For sensitive entries the value itself is never
  // returned by the API.
  has_value: boolean
  // A value IS stored but decrypts with none of the server's configured keys
  // (a key rotation that dropped the old key too early, a restored dump). The
  // entry is delivered to nothing until it is re-set — has_value alone cannot
  // say that, since an empty value looks identical.
  unreadable?: boolean
  // Sensitive entries are write-only; non-sensitive env entries ("variables")
  // include their value for display and inline editing.
  sensitive: boolean
  value?: string
  // Free-form purpose labels (lowercased, max 10).
  tags: string[]
  // Direct attachment target counts (tag-provided reach not included).
  ou_count: number
  node_count: number
  created_at: string
  updated_at: string
  last_used_at?: number
}

// count = items in this page, total = rows matching the filter.
export type SecretListResult = {
  secrets: Secret[]
  count: number
  total: number
  page: number
  per_page: number
}

// Server-side list filtering/sorting/pagination.
export type SecretListParams = {
  search?: string
  kind?: SecretKind
  sensitive?: boolean
  // AND semantics: secrets must carry all of these.
  tags?: string[]
  // Secrets delivered at this OU (attached to it or to any node inside it).
  ou_id?: string
  // Secrets delivered to this node (directly or via tag).
  node_id?: string
  // Only secrets with no attachment at all.
  unattached?: boolean
  sort?: 'name' | 'created_at' | 'updated_at'
  order?: 'asc' | 'desc'
  page?: number
  per_page?: number
}

export type CreateSecretRequest = {
  name: string
  kind: SecretKind
  key: string
  value: string
  // Defaults to true server-side; only env entries may be non-sensitive.
  sensitive?: boolean
  tags?: string[]
}

export type UpdateSecretRequest = {
  name?: string
  key?: string
  // Omitted = unchanged, "" = set empty, a value = set.
  value?: string
  // true->false is rejected by the API; false->true is allowed.
  sensitive?: boolean
  // Omitted = unchanged, [] = clear, list = replace.
  tags?: string[]
}

// A subject (secret XOR tag) attached to a target (OU XOR node).
export type SecretAttachment = {
  id: string
  secret_id?: string
  tag?: string
  ou_id?: string
  node_id?: string
}

export type SecretAttachmentListResult = {
  attachments: SecretAttachment[]
  count: number
}

// Attach a secret/tag to exactly one of an OU or a node.
export type AttachSecretRequest = {
  ou_id?: string
  node_id?: string
}

// Provenance of an entry in a target's effective secret set.
export type EffectiveSecretSource = {
  level: 'node' | 'ou' | 'root'
  via: 'direct' | 'tag'
  tag?: string
  ou_id?: string
}

export type EffectiveSecret = {
  id: string
  name: string
  kind: SecretKind
  key: string
  sensitive: boolean
  // Present only for non-sensitive entries.
  value?: string
  source: EffectiveSecretSource
}

export type EffectiveSecretListResult = {
  secrets: EffectiveSecret[]
  count: number
}

// Declarative replace of a target's DIRECT attachments: listed subjects are
// attached (idempotently), missing ones detached.
export type ReplaceSecretAttachmentsRequest = {
  secret_ids: string[]
  tags: string[]
}

// Tag facet for the filter rail.
export type SecretTagCount = {
  tag: string
  // Palette color: explicit override or a deterministic default per tag name.
  color: string
  count: number
  // Delivery target counts when the tag is attached somewhere.
  ou_count: number
  node_count: number
}

export type UpsertSecretTagRequest = {
  name: string
  color: string
}

export type UpdateSecretTagRequest = {
  name?: string
  color?: string
}

export type SecretTagListResult = {
  tags: SecretTagCount[]
  count: number
}

// OU facet for the filter tree: delivered-secret counts per OU.
export type SecretOUCount = {
  ou_id: string
  count: number
}

export type SecretOUCountsResult = {
  ous: SecretOUCount[]
  unattached: number
  total: number
}
