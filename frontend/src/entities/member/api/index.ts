import { apiClient } from '@/shared/utils/apiConfig'

import {
  BindingSpec,
  Invite,
  InviteCreated,
  Member,
  MemberRole,
  MemberStatus,
  RoleBinding,
} from './types'

type ApiResponse<T> = {
  success: boolean
  message: string
  data: T
  timestamp: string
}

const listMembers = async (): Promise<Member[]> => {
  const res = await apiClient.get<ApiResponse<Member[]>>('/api/v1/members')
  return res.data.data
}

// expectedBindingIds are the ids of the bindings the caller read for this
// member; the server refuses the replace with 409 member_bindings_stale when
// they no longer match (every replace issues new ids). A backend that
// predates the field ignores it.
const replaceBindings = async (
  userId: string,
  bindings: BindingSpec[],
  expectedBindingIds?: string[],
): Promise<RoleBinding[]> => {
  const res = await apiClient.put<ApiResponse<RoleBinding[]>>(
    `/api/v1/members/${userId}/bindings`,
    { bindings, expected_binding_ids: expectedBindingIds },
  )
  return res.data.data
}

const updateStatus = async (
  userId: string,
  status: MemberStatus,
): Promise<void> => {
  await apiClient.put(`/api/v1/members/${userId}/status`, { status })
}

const removeMember = async (userId: string): Promise<void> => {
  await apiClient.delete(`/api/v1/members/${userId}`)
}

const listInvites = async (): Promise<Invite[]> => {
  const res = await apiClient.get<ApiResponse<Invite[]>>(
    '/api/v1/members/invites',
  )
  return res.data.data
}

const createInvite = async (payload: {
  email: string
  role: MemberRole
  ou_id?: string
}): Promise<InviteCreated> => {
  const res = await apiClient.post<ApiResponse<InviteCreated>>(
    '/api/v1/members/invites',
    payload,
  )
  return res.data.data
}

const revokeInvite = async (inviteId: string): Promise<void> => {
  await apiClient.delete(`/api/v1/members/invites/${inviteId}`)
}

// resendInvite mints a NEW token for a pending invite and re-mails it. The old
// link stops working: the stored token is only a digest, so there is nothing to
// re-send, and keeping the previous link alive after a resend would be the
// wrong default anyway.
const resendInvite = async (inviteId: string): Promise<InviteCreated> => {
  const res = await apiClient.post<ApiResponse<InviteCreated>>(
    `/api/v1/members/invites/${inviteId}/resend`,
  )
  return res.data.data
}

export const apiMembers = {
  listMembers,
  replaceBindings,
  updateStatus,
  removeMember,
  listInvites,
  createInvite,
  revokeInvite,
  resendInvite,
}
