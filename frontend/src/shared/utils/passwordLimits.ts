// The server hashes passwords with bcrypt, which refuses input longer than 72
// BYTES. The limit is on the UTF-8 encoding, not the character count, so a
// passphrase in a non-Latin script reaches it in far fewer characters.
export const MAX_PASSWORD_BYTES = 72

export const passwordByteLength = (password: string): number =>
  new TextEncoder().encode(password).length

export const passwordTooLong = (password: string): boolean =>
  passwordByteLength(password) > MAX_PASSWORD_BYTES
