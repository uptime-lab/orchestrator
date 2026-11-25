export type MemberRole = 'viewer' | 'manager' | 'admin' | 'super_admin'

export type MemberStatus = 'active' | 'disabled'

export type RoleBinding = {
  id: string
  role: MemberRole
  ou_id?: string
}

export type Member = {
  user_id: string
  name: string
  email: string
  status: MemberStatus
  bindings: RoleBinding[]
  created_at: string
}

export type Invite = {
  id: string
  email: string
  role: MemberRole
  ou_id?: string
  expires_at: string
  created_at: string
}

export type InviteCreated = {
  invite: Invite
  token: string
  // delivered reports whether the link was also emailed. The token is returned
  // either way — installs without SMTP depend on the copyable link entirely.
  delivered: boolean
}

export type BindingSpec = {
  role: MemberRole
  ou_id?: string
}
