import type { SSHPreflightResult } from '@/entities/node/api/postSSHPreflight'

// The host key the operator trusts for the target they are adopting over SSH,
// held SEPARATELY from the latest connection-test result. The two used to be
// one thing (the trusted key was derived from the last result), and that is
// how a generic failed probe — device off, network blip — erased the trust: a
// retry then carried no expectation, the server took whatever key answered,
// and the adopt call offered credentials to it. Trust now moves only through
// the transitions below, never as a side effect of a probe failing.
export type HostKeyTrust = {
  fingerprint: string
  // The server's proof that this operator saw this key on this host:port; the
  // adopt call presents it. Refreshed by every probe that confirms the key.
  ticket?: string
  ticketExpiresAt?: string
  // The key was taken on first use and nothing has vouched for it since.
  firstContact: boolean
}

export type TrustTransition =
  | 'unchanged'
  | 'established'
  | 'refreshed'
  | 'replaced'
  | 'kept-through-failure'
  | 'kept-through-change'

export type TrustUpdate = {
  trust: HostKeyTrust | null
  transition: TrustTransition
}

// applyPreflightResult folds one connection-test result into the trust state.
// `accepted` says the probe was sent with accept_new_host_key: only then may
// a different key replace the trusted one, and only because the operator
// clicked the action that says so.
export const applyPreflightResult = (
  prev: HostKeyTrust | null,
  result: SSHPreflightResult,
  accepted = false,
): TrustUpdate => {
  // A changed key is a report, never a replacement.
  if (result.host_key_changed) {
    return {
      trust: prev,
      transition: prev ? 'kept-through-change' : 'unchanged',
    }
  }
  // A probe that reached no key (device off, timeout, wrong password) says
  // nothing about the key and must not forget one.
  if (!result.reachable || !result.host_key_fingerprint) {
    return {
      trust: prev,
      transition: prev ? 'kept-through-failure' : 'unchanged',
    }
  }
  const fresh: HostKeyTrust = {
    fingerprint: result.host_key_fingerprint,
    ticket: result.host_key_ticket,
    ticketExpiresAt: result.host_key_ticket_expires_at,
    firstContact: !!result.first_contact,
  }
  if (!prev) {
    return { trust: fresh, transition: 'established' }
  }
  if (prev.fingerprint === result.host_key_fingerprint) {
    // The same key again: the ticket is renewed and the key is no longer a
    // first-contact one, whatever the server said (it was checked against
    // an expectation this time).
    return {
      trust: { ...fresh, firstContact: false },
      transition: 'refreshed',
    }
  }
  if (accepted || result.host_key_reset) {
    return { trust: { ...fresh, firstContact: false }, transition: 'replaced' }
  }
  // A different key arrived without the operator asking for it. The probe
  // should have been sent with the expectation and come back as
  // host_key_changed; treat the surprise as the report it should have been.
  return { trust: prev, transition: 'kept-through-change' }
}

// The expectation the next probe should carry: the trusted key, or nothing
// when no key is trusted yet (first contact).
export const expectedFingerprint = (
  trust: HostKeyTrust | null,
): string | undefined => trust?.fingerprint || undefined

// ticketIsLive says whether the trust's ticket can still be presented; the
// server also checks, so this only spares the operator a round trip.
export const ticketIsLive = (
  trust: HostKeyTrust | null,
  now = Date.now(),
): boolean => {
  if (!trust?.ticket) return false
  if (!trust.ticketExpiresAt) return true
  const exp = Date.parse(trust.ticketExpiresAt)
  return Number.isNaN(exp) || exp > now
}
