import type React from 'react'
import { useEffect, useState } from 'react'

// A reboot round-trip (shutdown, POST, boot, agent reconnect) takes about
// this long on a Raspberry Pi class board; the timed bar is an expectation indicator,
// not a measurement — completion is still signaled by the node reconnecting.
export const REBOOT_EXPECTED_MS = 100_000
// The netboot bootstrap round-trip averages slightly longer than a disk boot.
export const RECOVERY_REBOOT_EXPECTED_MS = 110_000
// Leaving the bootstrap for the installed OS is quicker (no HTTP boot chain).
export const RECOVERY_TO_OS_EXPECTED_MS = 60_000

// 0..1 progress of a fixed-duration expectation window starting at startISO.
// Ticks once a second while a start time is set.
export const useTimedProgress = (
  startISO: string | undefined,
  durationMs: number = REBOOT_EXPECTED_MS,
): number => {
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    if (!startISO) return undefined
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [startISO])

  if (!startISO) return 0
  const start = new Date(startISO).getTime()
  if (Number.isNaN(start)) return 0
  return Math.max(0, Math.min(1, (now - start) / durationMs))
}

const ORANGE = [255, 154, 61] // orange-6
const VIOLET = [142, 102, 255] // violet-5 (the recovery neon)

const mixChannels = (t: number): number[] =>
  ORANGE.map((from, i) => Math.round(from + (VIOLET[i] - from) * t))

// Color of the reboot-to-recovery transition at progress t: the closer the
// node is to coming back netbooted, the more violet it gets.
export const rebootToRecoveryColor = (t: number): string => {
  const [r, g, b] = mixChannels(t)
  return `rgb(${r}, ${g}, ${b})`
}

// Inline style tinting the status badge along the same transition (the badge
// classes stay for layout; these override the colors).
export const rebootToRecoveryBadgeStyle = (t: number): React.CSSProperties => {
  const [r, g, b] = mixChannels(t)
  return {
    color: `rgb(${r}, ${g}, ${b})`,
    borderColor: `rgba(${r}, ${g}, ${b}, 0.5)`,
    backgroundColor: `rgba(${r}, ${g}, ${b}, 0.1)`,
    boxShadow: `0 0 14px rgba(${r}, ${g}, ${b}, 0.35)`,
  }
}
