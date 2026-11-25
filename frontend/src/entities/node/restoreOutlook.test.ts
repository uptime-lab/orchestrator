import { describe, expect, it } from 'vitest'

// The words come from the locale file, so the bundle has to be initialised
// before the first t() call — otherwise every assertion reads a key back.
import '@/i18n'

import type { DeletedNode } from '@/entities/node/api/deletedNodes'

import { deviceStillTrying, restoreAffordance } from './restoreOutlook'

const row = (over: Partial<DeletedNode> = {}): DeletedNode => ({
  id: 'n1',
  name: 'Node-abcd0002',
  hostname: 'ubuntu',
  hardware_serial: '10000000abcd0002',
  adoption_method: 'eeprom',
  deleted_at: '2026-09-01T10:13:23Z',
  deleted_by: 'u1',
  restorable: true,
  ...over,
})

describe('restoreAffordance', () => {
  // The board was adopted again as a new node in this account: the restore is
  // refused until that node is deleted, and the reason names it.
  it('disables the restore and names the live node when the board is another node here', () => {
    const got = restoreAffordance(
      row({
        restorable: false,
        serial_bound_in_account: { node_id: 'n2', name: 'bench-pi' },
        serial_bound_elsewhere: true,
      }),
    )

    expect(got.canRestore).toBe(false)
    expect(got.tone).toBe('warning')
    expect(got.text).toContain('"bench-pi"')
    expect(got.text).toContain('Delete that node first')
    expect(got.blockedReason).toBe(got.text)
  })

  it('treats a null holder as none', () => {
    expect(
      restoreAffordance(row({ serial_bound_in_account: null })).canRestore,
    ).toBe(true)
  })

  // An EEPROM-adopted node whose device has already been re-adopted into
  // another account, otherwise shown under the most confident sentence in
  // the panel.
  it('refuses to promise a rejoin when the serial moved, whatever the adoption method', () => {
    const got = restoreAffordance(row({ serial_bound_elsewhere: true }))

    expect(got.canRestore).toBe(false)
    expect(got.tone).toBe('warning')
    expect(got.text).not.toContain('restoring is enough')
    expect(got.text).toContain('registered to another account')
  })

  // The platform knows a serial binding. It does not know the device moved — the
  // squatting case is exactly where it did not — so the copy must not say so.
  it('claims a serial binding, never a device movement', () => {
    const got = restoreAffordance(row({ serial_bound_elsewhere: true }))

    expect(got.text).toMatch(/serial/i)
    expect(got.text).not.toMatch(/device has (re-)?joined/i)
  })

  it('overrides the identity-release caveat, which is about a copy that no longer matters', () => {
    const got = restoreAffordance(
      row({
        adoption_method: 'file',
        identity_release_requested: true,
        serial_bound_elsewhere: true,
      }),
    )

    expect(got.canRestore).toBe(false)
    expect(got.text).toContain('registered to another account')
  })

  it('treats an absent flag as not bound, so an older backend still offers a restore', () => {
    const got = restoreAffordance(row({ serial_bound_elsewhere: undefined }))

    expect(got.canRestore).toBe(true)
    expect(got.text).toContain('EEPROM')
  })

  it('keeps the existing wording for a row whose serial is free', () => {
    expect(restoreAffordance(row()).canRestore).toBe(true)
    expect(restoreAffordance(row({ adoption_method: 'file' })).text).toContain(
      'file on the device',
    )
    expect(
      restoreAffordance(row({ adoption_method: 'unknown' })).text,
    ).toContain('never reported hardware')
  })

  it('still warns about a released on-disk identity when the serial is free', () => {
    const got = restoreAffordance(
      row({ adoption_method: 'file', identity_release_requested: true }),
    )

    expect(got.canRestore).toBe(true)
    expect(got.tone).toBe('warning')
  })
})

describe('restoreAffordance — what the record knows about the identity', () => {
  // HTTP_HOST alone says the EEPROM points at a platform. The strong promise
  // needs the platform's own flash of THIS node's token.
  it('promises a self-rejoin only for a confirmed bake', () => {
    const unconfirmed = restoreAffordance(row({ adoption_method: 'eeprom' }))
    expect(unconfirmed.text).not.toContain('rejoins on its own')
    expect(unconfirmed.canRestore).toBe(true)

    const confirmed = restoreAffordance(
      row({ adoption_method: 'eeprom', eeprom_bake_confirmed: true }),
    )
    expect(confirmed.text).toContain('rejoins on its own')
  })

  it('warns for a requested release on a baked device that boots its disk first', () => {
    const got = restoreAffordance(
      row({
        adoption_method: 'eeprom',
        eeprom_bake_confirmed: true,
        boot_order_netboots_first: false,
        identity_release_requested: true,
      }),
    )
    expect(got.tone).toBe('warning')
    expect(got.canRestore).toBe(true)
    expect(got.text).toMatch(/re-adopt/i)
  })

  it('keeps the footnote tone for a requested release on a netboot-first baked device', () => {
    const got = restoreAffordance(
      row({
        adoption_method: 'eeprom',
        eeprom_bake_confirmed: true,
        boot_order_netboots_first: true,
        identity_release_requested: true,
      }),
    )
    expect(got.tone).toBe('neutral')
  })

  it("reports a confirmed erase as the device's claim and stops promising the file", () => {
    const got = restoreAffordance(
      row({
        adoption_method: 'file',
        identity_release_requested: true,
        identity_released_at: '2026-09-04T14:35:17Z',
      }),
    )
    expect(got.tone).toBe('warning')
    expect(got.text).toMatch(/reported/i)
    expect(got.text).not.toContain('while that file exists')
  })

  it('still lets a netboot-first baked device rejoin after a confirmed erase', () => {
    const got = restoreAffordance(
      row({
        adoption_method: 'eeprom',
        eeprom_bake_confirmed: true,
        boot_order_netboots_first: true,
        identity_release_requested: true,
        identity_released_at: '2026-09-04T14:35:17Z',
      }),
    )
    expect(got.tone).toBe('neutral')
    expect(got.text).toContain('rejoins on its own')
  })

  it('lets a bound-elsewhere serial override even a confirmed erase', () => {
    const got = restoreAffordance(
      row({
        identity_released_at: '2026-09-04T14:35:17Z',
        serial_bound_elsewhere: true,
      }),
    )
    expect(got.canRestore).toBe(false)
  })
})

describe('deviceStillTrying', () => {
  const now = Date.parse('2026-09-04T15:00:00Z')

  it('is true within the window and false outside or when absent', () => {
    expect(
      deviceStillTrying({ last_refused_at: '2026-09-04T14:30:00Z' }, now),
    ).toBe(true)
    expect(
      deviceStillTrying({ last_refused_at: '2026-09-04T13:00:00Z' }, now),
    ).toBe(false)
    expect(deviceStillTrying({}, now)).toBe(false)
    expect(deviceStillTrying({ last_refused_at: 'garbage' }, now)).toBe(false)
  })
})
