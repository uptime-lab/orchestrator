// Board identity for a Raspberry Pi node.
//
// One place, because there used to be four: `deviceModelLabel` and a
// `socFromCPUModel` in the node sidebar, a second copy of `socFromCPUModel` in
// the bootloader section, and `nodeSoC` driving the fleet list. They disagreed
// as soon as a board other than a CM4 or CM5 appeared — the same node could
// read "Pi 5" in the sidebar and "CM5" in the fleet column.
//
// Prefer the board revision code over the reported model string. The model
// comes from the device tree, and the recovery bootstrap forces ONE device_tree
// for all of BCM2712, so every Pi 5 running it describes itself as a Compute
// Module 5. The revision code is written by the firmware from OTP and is
// unaffected. Agents older than 0.4.5 send no revision, hence the fallback.

export type BoardLabel =
  | 'Pi 4'
  | 'Pi 400'
  | 'CM4'
  | 'CM4S'
  | 'Pi 5'
  | 'Pi 500'
  | 'CM5'
  | 'CM5 Lite'
  | 'CM3'
  | 'Pi 3'
  | 'Pi Zero 2'

// Board type field of a new-style revision code (bits 4-11).
const BOARD_TYPES: Record<number, BoardLabel> = {
  0x11: 'Pi 4',
  0x13: 'Pi 400',
  0x14: 'CM4',
  0x15: 'CM4S',
  0x17: 'Pi 5',
  0x18: 'CM5',
  0x19: 'Pi 500',
  0x1a: 'CM5 Lite',
}

type DecodedRevision = { label: BoardLabel | null; soc: 'cm4' | 'cm5' | null }

// decodeBoardRevision reads a raw code such as "c04170". The layout is only
// valid when bit 23 (the new-style flag) is set — an old sequential code would
// decode to nonsense — so anything else yields nulls and the caller falls back.
export const decodeBoardRevision = (rev?: string): DecodedRevision => {
  const none: DecodedRevision = { label: null, soc: null }
  if (!rev) return none
  const cleaned = rev.trim().replace(/^0x/i, '')
  if (!/^[0-9a-fA-F]{1,8}$/.test(cleaned)) return none
  const code = parseInt(cleaned, 16)
  if (Number.isNaN(code) || (code & (1 << 23)) === 0) return none

  const processor = (code >> 12) & 0xf
  return {
    label: BOARD_TYPES[(code >> 4) & 0xff] ?? null,
    // 3 = BCM2711, 4 = BCM2712. Read from the processor nibble rather than the
    // type table so a board released after this code still resolves its SoC.
    soc: processor === 3 ? 'cm4' : processor === 4 ? 'cm5' : null,
  }
}

// boardLabelFromModel is the pre-revision-code path. Order matters: the longer
// names must be tested before their own prefixes, or a CM5 Lite reads as a CM5.
export const boardLabelFromModel = (model?: string): BoardLabel | null => {
  if (!model) return null
  const m = model.toLowerCase()
  if (m.includes('compute module 5 lite')) return 'CM5 Lite'
  if (m.includes('compute module 5')) return 'CM5'
  if (m.includes('compute module 4s')) return 'CM4S'
  if (m.includes('compute module 4')) return 'CM4'
  if (m.includes('compute module 3')) return 'CM3'
  if (m.includes('raspberry pi 500')) return 'Pi 500'
  if (m.includes('raspberry pi 5')) return 'Pi 5'
  if (m.includes('raspberry pi 400')) return 'Pi 400'
  if (m.includes('raspberry pi 4')) return 'Pi 4'
  if (m.includes('raspberry pi zero 2')) return 'Pi Zero 2'
  if (m.includes('raspberry pi 3')) return 'Pi 3'
  return null
}

const socFromModel = (model?: string): 'cm4' | 'cm5' | null => {
  const label = boardLabelFromModel(model)
  if (!label) return null
  if (
    label === 'CM5' ||
    label === 'CM5 Lite' ||
    label === 'Pi 5' ||
    label === 'Pi 500'
  )
    return 'cm5'
  if (
    label === 'CM4' ||
    label === 'CM4S' ||
    label === 'Pi 4' ||
    label === 'Pi 400'
  )
    return 'cm4'
  return null
}

// boardLabel is the short board name shown in the UI, or null for hardware this
// platform does not recognise (there is nothing meaningful to shorten).
export const boardLabel = (
  model?: string,
  boardRevision?: string,
): BoardLabel | null =>
  decodeBoardRevision(boardRevision).label ?? boardLabelFromModel(model)

// boardSoC is the EEPROM firmware family — the silicon, not the form factor.
// A Pi 4 B answers 'cm4' here and that is correct; it is NOT a statement that
// config.txt's [cm4] filter applies to it.
export const boardSoC = (
  model?: string,
  boardRevision?: string,
): 'cm4' | 'cm5' | null =>
  decodeBoardRevision(boardRevision).soc ?? socFromModel(model)
