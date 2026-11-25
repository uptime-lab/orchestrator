import { UserEntity, UserRole } from './types'

export type ApiUserResult = {
  id: string
  email: string
  name: string
  // email_verified reports proven mailbox ownership. Absent on responses from
  // a backend predating verification — treat that as "not verified".
  email_verified?: boolean
  // The stored acceptance of the operator's terms. The wire name is the one
  // the field was introduced under.
  beta_notice_acknowledged?: boolean
  created_at: string
  updated_at: string
}

/**
 * Maps API UserResult to UserEntity
 * Uses defaults for fields not provided by the API
 */
export const mapApiUserToEntity = (apiUser: ApiUserResult): UserEntity => {
  // Parse name - API provides single "name" field, we split into name and surname
  const nameParts = apiUser.name.trim().split(/\s+/)
  const name = nameParts[0] || ''
  const surname = nameParts.slice(1).join(' ') || ''

  // Parse dates - API provides ISO strings; entity timestamps are in seconds
  // (consumers render them via getDateFromSeconds).
  const created_at = Math.floor(new Date(apiUser.created_at).getTime() / 1000)
  const updated_at = Math.floor(new Date(apiUser.updated_at).getTime() / 1000)

  return {
    id: apiUser.id,
    name,
    surname,
    email: apiUser.email,
    email_verified: apiUser.email_verified === true,
    terms_accepted: apiUser.beta_notice_acknowledged === true,
    nickname: undefined,
    role: UserRole.Normal, // Default role, API doesn't provide this
    is_active: true, // Default to active, API doesn't provide this
    image_url: null,
    registered_at: created_at,
    last_visited_at: updated_at,
  }
}
