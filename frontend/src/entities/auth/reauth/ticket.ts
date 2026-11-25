// The live re-authentication ticket.
//
// Held in memory ONLY — never localStorage. A ticket is a short-lived elevation
// of the current tab, not a credential of the account: persisting it would
// survive a reload and hand the elevation to whoever opens the browser next,
// which is the very thing the confirmation exists to prevent.

// SAFETY_MARGIN_MS retires the ticket slightly before the server does, so a
// request never leaves with a ticket that expires in flight — which would
// surface as a confusing second prompt mid-ceremony.
const SAFETY_MARGIN_MS = 5_000

let ticket: string | null = null
let expiresAt = 0

export const setReauthTicket = (
  value: string,
  expiresInSeconds: number,
): void => {
  ticket = value
  expiresAt = Date.now() + expiresInSeconds * 1000 - SAFETY_MARGIN_MS
}

// getReauthTicket returns the ticket while it is still good, else null.
export const getReauthTicket = (): string | null => {
  if (!ticket || Date.now() >= expiresAt) {
    ticket = null
    return null
  }
  return ticket
}

// clearReauthTicket drops the elevation. Called on sign-out: the next user of
// this tab must confirm for themselves.
export const clearReauthTicket = (): void => {
  ticket = null
  expiresAt = 0
}
