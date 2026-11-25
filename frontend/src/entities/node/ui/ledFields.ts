import type {
  DigitalLEDSpec,
  LEDConfig,
  LEDSource,
} from '@/entities/node/api/led'

// Shared edit-state logic for the per-LED digital indication controls (LED
// v2): the node sidebar's LED panel and the OU policy form both render rows
// of {source, low, high, brightness}, and the tricky part — how an empty
// field becomes the wire's "0 = source default" without ever sending a lone
// zero the server refuses — must not exist twice.

// What an agent that advertises nothing can render (LED v1). The v2 sources
// (ram, disk) are offered only off the agent's own advertisement.
export const LED_V1_SOURCES: LEDSource[] = ['temperature', 'load']

// Every source the platform knows. The OU form offers all of them — a policy
// declares intent with no agent to consult; the per-node apply is what gates
// on the advertisement.
export const LED_ALL_SOURCES: LEDSource[] = [
  'off',
  'temperature',
  'load',
  'ram',
  'disk',
]

// Per-source gradient defaults — the agent's built-in constants, shown as
// placeholders so an empty field reads as "the default", not as zero.
export const ledSourceDefaults = (
  s: LEDSource,
): { low: number; high: number } =>
  s === 'temperature' ? { low: 45, high: 80 } : { low: 0, high: 100 }

export const LED_DEFAULT_BRIGHTNESS = 20

// One digital LED's edit state. The numeric fields are strings because they
// are controlled inputs where '' means "use the source default" — a meaning
// the wire encodes as 0, which for the percentage sources is also a real low
// bound, so rowToSpec resolves '' per pair, not per field.
export type LEDRow = {
  source: LEDSource
  low: string
  high: string
  brightness: string
}

// The wire spec for one row. The low/high pair travels explicitly as soon as
// either is set (the backend treats low=0,high=0 as "defaults", anything else
// as an explicit pair), so a single filled field borrows the source default
// for its partner instead of sending a 0 the server would refuse.
export const ledRowToSpec = (row: LEDRow): DigitalLEDSpec => {
  const d = ledSourceDefaults(row.source)
  const lowSet = row.low.trim() !== ''
  const highSet = row.high.trim() !== ''
  return {
    source: row.source,
    low: lowSet ? Number(row.low) : highSet ? d.low : 0,
    high: highSet ? Number(row.high) : lowSet ? d.high : 0,
    // Brightness is a whole percentage on the wire; a decimal would refuse
    // the whole save as a malformed request.
    brightness:
      row.brightness.trim() !== '' ? Math.round(Number(row.brightness)) : 0,
  }
}

// Rows for a desired config, padded to the board's LED count. Accepts either
// form: digital_leds (v2) is the source of truth when present, else the v1
// sources render with default bounds.
export const ledConfigToRows = (
  cfg: LEDConfig | undefined,
  count: number,
): LEDRow[] => {
  const specs: DigitalLEDSpec[] =
    cfg?.digital_leds ??
    (cfg?.digital_sources ?? []).map((s) => ({ source: s }))
  return Array.from({ length: count }, (_, i) => {
    const spec = specs[i]
    const pairSet = !!spec && ((spec.low ?? 0) !== 0 || (spec.high ?? 0) !== 0)
    return {
      source: spec?.source ?? 'off',
      low: pairSet ? String(spec.low ?? 0) : '',
      high: pairSet ? String(spec.high ?? 0) : '',
      brightness: spec?.brightness ? String(spec.brightness) : '',
    }
  })
}

// Mirror of the server's bounds so a refusal is not the first feedback: an
// explicit pair must ascend, brightness is 0..100 (0 / empty = default).
export const ledSpecsInvalidRange = (specs: DigitalLEDSpec[]): boolean =>
  specs.some(
    (s) =>
      s.source !== 'off' &&
      ((s.low ?? 0) !== 0 || (s.high ?? 0) !== 0) &&
      (s.high ?? 0) <= (s.low ?? 0),
  )

export const ledSpecsInvalidBrightness = (specs: DigitalLEDSpec[]): boolean =>
  specs.some((s) => (s.brightness ?? 0) < 0 || (s.brightness ?? 0) > 100)
