// Account is the team/company the current user is signed into.
export type Account = {
  id: string
  name: string
  // require_two_factor: every member who signs in with a password must enroll
  // a second factor before they can use this account; members with no
  // password (Google only) are not asked.
  require_two_factor: boolean
  // identify_duration_sec: how long identify blinks run account-wide.
  identify_duration_sec?: number
  // audit_retention_days: how long this account's audit trail is kept, in
  // days; 0 = indefinitely. Read-only: set by instance administration.
  audit_retention_days?: number
  created_at: string
  updated_at: string
}

// Name bounds mirror the server-side command validation.
export const ACCOUNT_NAME_MIN = 2
export const ACCOUNT_NAME_MAX = 100
