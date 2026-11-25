import { describe, expect, it } from 'vitest'

import type { SSHPreflightResult } from '@/entities/node/api/postSSHPreflight'

import {
  applyPreflightResult,
  expectedFingerprint,
  ticketIsLive,
  type HostKeyTrust,
} from './sshHostKeyTrust'

const keyA = 'SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const keyB = 'SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'

const reachable = (
  over: Partial<SSHPreflightResult> = {},
): SSHPreflightResult => ({
  reachable: true,
  host_key_fingerprint: keyA,
  host_key_ticket: 'ticket-1',
  host_key_ticket_expires_at: '2026-09-08T12:15:00Z',
  first_contact: true,
  user: 'pi',
  arch: 'aarch64',
  arch_supported: true,
  sudo_mode: 'nopasswd',
  cdn_reachable: true,
  cdn_checked: true,
  ...over,
})

const unreachable = (
  over: Partial<SSHPreflightResult> = {},
): SSHPreflightResult => ({
  reachable: false,
  error: 'dial 192.0.2.3:22: connection refused',
  host_key_fingerprint: '',
  user: '',
  arch: '',
  arch_supported: false,
  sudo_mode: '',
  cdn_reachable: false,
  cdn_checked: false,
  ...over,
})

const trustA: HostKeyTrust = {
  fingerprint: keyA,
  ticket: 'ticket-1',
  ticketExpiresAt: '2026-09-08T12:15:00Z',
  firstContact: true,
}

describe('applyPreflightResult', () => {
  it('establishes trust from a first-contact probe and says so', () => {
    const got = applyPreflightResult(null, reachable())
    expect(got.transition).toBe('established')
    expect(got.trust).toEqual(trustA)
  })

  // The scenario: key A seen, then the device drops, then a retry.
  // The failed probe carries no fingerprint and no host_key_changed flag; it
  // used to replace the trusted key with nothing.
  it('keeps key A through a generic failure so the retry still expects it', () => {
    const afterDrop = applyPreflightResult(trustA, unreachable())
    expect(afterDrop.transition).toBe('kept-through-failure')
    expect(afterDrop.trust).toEqual(trustA)
    expect(expectedFingerprint(afterDrop.trust)).toBe(keyA)
  })

  it('keeps key A through a timeout that reports nothing at all', () => {
    const got = applyPreflightResult(
      trustA,
      unreachable({ error: 'ssh handshake: i/o timeout' }),
    )
    expect(got.trust).toEqual(trustA)
  })

  it('does not replace the key on a host_key_changed report', () => {
    const got = applyPreflightResult(
      trustA,
      unreachable({
        host_key_changed: true,
        host_key_fingerprint: keyB,
        expected_host_key_fingerprint: keyA,
      }),
    )
    expect(got.transition).toBe('kept-through-change')
    expect(got.trust).toEqual(trustA)
  })

  it('does not replace the key when a different one arrives without being asked for', () => {
    const got = applyPreflightResult(
      trustA,
      reachable({ host_key_fingerprint: keyB }),
    )
    expect(got.transition).toBe('kept-through-change')
    expect(got.trust).toEqual(trustA)
  })

  it('replaces the key only when the probe was sent as an explicit accept', () => {
    const got = applyPreflightResult(
      trustA,
      reachable({
        host_key_fingerprint: keyB,
        host_key_ticket: 'ticket-2',
        first_contact: false,
        host_key_reset: true,
        expected_host_key_fingerprint: keyA,
      }),
      true,
    )
    expect(got.transition).toBe('replaced')
    expect(got.trust).toEqual({
      fingerprint: keyB,
      ticket: 'ticket-2',
      ticketExpiresAt: '2026-09-08T12:15:00Z',
      firstContact: false,
    })
  })

  it('refreshes the ticket when the same key is confirmed and drops first-contact', () => {
    const got = applyPreflightResult(
      trustA,
      reachable({
        host_key_ticket: 'ticket-2',
        host_key_ticket_expires_at: '2026-09-08T12:30:00Z',
        first_contact: false,
      }),
    )
    expect(got.transition).toBe('refreshed')
    expect(got.trust).toEqual({
      fingerprint: keyA,
      ticket: 'ticket-2',
      ticketExpiresAt: '2026-09-08T12:30:00Z',
      firstContact: false,
    })
  })

  it('reports no transition when nothing was trusted and nothing was learned', () => {
    expect(applyPreflightResult(null, unreachable())).toEqual({
      trust: null,
      transition: 'unchanged',
    })
  })
})

describe('expectedFingerprint', () => {
  it('is undefined before first contact so the probe may take the key', () => {
    expect(expectedFingerprint(null)).toBeUndefined()
  })
  it('is the trusted key afterwards', () => {
    expect(expectedFingerprint(trustA)).toBe(keyA)
  })
})

describe('ticketIsLive', () => {
  const t0 = Date.parse('2026-09-08T12:00:00Z')
  it('is false without a ticket', () => {
    expect(ticketIsLive(null, t0)).toBe(false)
    expect(ticketIsLive({ ...trustA, ticket: undefined }, t0)).toBe(false)
  })
  it('follows the expiry the server stated', () => {
    expect(ticketIsLive(trustA, t0)).toBe(true)
    expect(ticketIsLive(trustA, Date.parse('2026-09-08T12:15:01Z'))).toBe(false)
  })
})
