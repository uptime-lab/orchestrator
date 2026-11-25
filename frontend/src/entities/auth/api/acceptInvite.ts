import { apiClient, publicApiClient } from '@/shared/utils/apiConfig'

import { ApiResponse } from './postSignIn'

export type AcceptedInvite = {
  id: string
  email: string
  role: string
  ou_id?: string
}

// postAcceptInvite joins the SIGNED-IN user to the invite's account. This is
// the counterpart of postInviteSignup: the same link serves both a brand-new
// teammate (signup) and someone who already has an Orchestrator identity and is
// being added to a second account.
export const postAcceptInvite = async (
  token: string,
): Promise<AcceptedInvite> => {
  const response = await apiClient.post<ApiResponse<AcceptedInvite>>(
    '/api/v1/invites/accept',
    { token },
  )
  return response.data.data
}

export type InvitePreview = {
  email: string
  account_name: string
  role: string
  // user_exists distinguishes "sign in to accept" from "create your account".
  // Without it the page can only guess from the session, and guessing wrong
  // walks an existing user into a signup the server is bound to reject.
  user_exists: boolean
}

// getInvitePreview describes an invitation before anyone accepts it. Public:
// the token is the credential, and its holder was just emailed the address.
export const getInvitePreview = async (
  token: string,
): Promise<InvitePreview> => {
  const response = await publicApiClient.get<ApiResponse<InvitePreview>>(
    `/auth/invite/preview?token=${encodeURIComponent(token)}`,
  )
  return response.data.data
}
