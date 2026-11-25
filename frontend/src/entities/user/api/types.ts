export enum UserRole {
  Request = 0,
  Normal = 1,
  Admin = 10,
}

export type UserEntity = {
  id: string
  name: string
  surname: string
  email: string
  email_verified: boolean
  // terms_accepted: the backend holds this user's acceptance of the operator's
  // terms. False keeps the acceptance dialog up on an install that set terms.
  terms_accepted: boolean
  nickname?: string
  role: UserRole
  is_active: boolean
  image_url?: string | null
  registered_at: number
  last_visited_at: number | null
}
