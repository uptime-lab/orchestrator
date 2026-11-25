// Display-name rules the server enforces (domain/user.MaxNameLength and the
// update command's non-blank check), checked before a request is sent.
export const MAX_DISPLAY_NAME_LENGTH = 256

export type DisplayNameProblem = 'required' | 'tooLong' | null

export const displayNameProblem = (name: string): DisplayNameProblem => {
  if (name.trim() === '') return 'required'
  if (name.length > MAX_DISPLAY_NAME_LENGTH) return 'tooLong'
  return null
}
