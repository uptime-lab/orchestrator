// The wait before the next WebSocket reconnect: doubling from one second to a
// ceiling of thirty, with a little jitter so the tabs of a whole office do not
// all reconnect in the same instant when the server comes back.
export const RECONNECT_BASE_DELAY_MS = 1_000
export const RECONNECT_MAX_DELAY_MS = 30_000
const JITTER = 0.2

export const reconnectDelayMs = (
  attempt: number,
  random: () => number = Math.random,
): number => {
  const steps = Math.max(0, Math.min(attempt, 16))
  const base = Math.min(
    RECONNECT_BASE_DELAY_MS * 2 ** steps,
    RECONNECT_MAX_DELAY_MS,
  )
  // Jitter only ever shortens the wait, so the ceiling stays a ceiling.
  return Math.round(base * (1 - JITTER * random()))
}
