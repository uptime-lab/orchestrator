export type MasterTokenResult = {
  id: string
  account_id: string
  name: string
  token?: string // Only present on creation
  adoption_file?: string // Base64 encoded, only present on creation
  is_active: boolean
  default_username?: string
  ssh_key_id?: string
  // Devices adopted with this token are placed into this OU.
  ou_id?: string
  tags: string[]
  expires_at?: number
  last_used_at?: number
  created_at: string
  updated_at: string
}

export type MasterTokenListResult = {
  tokens: MasterTokenResult[]
  count: number
}

export type CreateMasterTokenRequest = {
  name: string
  default_username: string
  default_password: string
  ssh_key_id?: string
  ou_id?: string
  tags?: string[]
  // RFC 3339 on requests; a token as read carries epoch seconds.
  expires_at?: string
}

export type UpdateMasterTokenRequest = {
  name?: string
  is_active?: boolean
  default_username?: string
  default_password?: string
  // Omitted = unchanged, "" = clear, UUID = set.
  ssh_key_id?: string
  // Omitted = unchanged, "" = clear, UUID = set the adoption target OU.
  ou_id?: string
  // Omitted = unchanged, [] = clear, list = replace.
  tags?: string[]
  // RFC 3339 on requests; a token as read carries epoch seconds.
  expires_at?: string
}
