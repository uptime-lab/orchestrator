export type SSHKeyType = 'ssh' | 'ca'

export type SSHKeyResult = {
  id: string
  account_id: string
  alias: string
  key_type: SSHKeyType
  fingerprint: string
  is_default: boolean
  tags: string[]
  // True for server-generated connection keypairs (the private key is held
  // encrypted and never returned; only the public part is downloadable).
  has_private_key?: boolean
  created_at: string
  updated_at: string
}

export type SSHKeyListResult = {
  keys: SSHKeyResult[]
  count: number
}
