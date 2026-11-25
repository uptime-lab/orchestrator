import { describe, expect, it } from 'vitest'

import '@/i18n'

import {
  createdInviteHint,
  deletionRequestHint,
  deletionRequestTooltip,
  pendingInviteNotEmailed,
  resentInviteHint,
} from './deletionHint'

describe('deletionRequestHint', () => {
  // Only the hosted install has an operator console that performs the
  // erasure; a self-hosted install records the request and nothing more.
  it('promises no erasure on a self-hosted install', () => {
    const got = deletionRequestHint(false)
    expect(got).toContain('operator of this install')
    expect(got).toContain('Nothing is deleted automatically')
    expect(got).not.toContain('performs the erasure')
  })

  it('keeps the hosted wording where the console performs it', () => {
    expect(deletionRequestHint(true)).toContain('performs the erasure')
  })

  it('keeps the hosted wording until capabilities load', () => {
    expect(deletionRequestHint(undefined)).toBe(deletionRequestHint(true))
  })
})

describe('deletionRequestTooltip', () => {
  it('describes no erasure on a self-hosted install', () => {
    const got = deletionRequestTooltip(false)
    expect(got).toContain('operator of this install')
    expect(got).not.toContain('The erasure removes')
    expect(got).not.toContain('instance operator performs')
  })

  it('keeps the erasure background where the console performs it', () => {
    expect(deletionRequestTooltip(true)).toContain('The erasure removes')
    expect(deletionRequestTooltip(undefined)).toBe(deletionRequestTooltip(true))
  })
})

describe('resentInviteHint', () => {
  it('says the invitation was sent only when it was', () => {
    expect(resentInviteHint(true)).toContain('was sent')
    const notSent = resentInviteHint(false)
    expect(notSent).not.toContain('was sent')
    expect(notSent).toContain('share it yourself')
  })
})

describe('createdInviteHint', () => {
  it('names the address when the invitation was emailed', () => {
    expect(createdInviteHint(true, 'a@example.com')).toContain('a@example.com')
  })

  it('says plainly that nothing was emailed when it was not', () => {
    const notSent = createdInviteHint(false, 'a@example.com')
    expect(notSent).toContain('did not email')
    expect(notSent).toContain('share it yourself')
    expect(notSent).toContain('only once')
  })
})

describe('pendingInviteNotEmailed', () => {
  it('is true only when the install cannot send mail', () => {
    expect(pendingInviteNotEmailed(false)).toBe(true)
    expect(pendingInviteNotEmailed(true)).toBe(false)
    expect(pendingInviteNotEmailed(undefined)).toBe(false)
  })
})
