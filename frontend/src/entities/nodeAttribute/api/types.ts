// AD-style custom node attributes: per-node key/value pairs. Sensitive
// attributes are write-only (value omitted, has_value reported); non-sensitive
// ones return their value for inline display/editing.
export type NodeAttribute = {
  secret_id: string
  key: string
  sensitive: boolean
  value?: string
  has_value: boolean
}

export type NodeAttributeListResult = {
  attributes: NodeAttribute[]
  count: number
}

export type UpsertNodeAttributeRequest = {
  value: string
  sensitive: boolean
}
