// Dismissing a button mark, keyed by VALUE rather than by two clocks.
//
// The physical-press mark lives in two places: this browser's store and the
// platform's registry, which replays it into every socket that connects inside
// its 10-minute TTL. The ID lamp's off-click clears the local one and — when
// nothing is blinking — deliberately dispatches nothing, so it reaches no
// endpoint and the registry keeps its `pressed_at`. The next reconnect then
// hydrated the mark straight back, and the operator saw a mark they had just
// dismissed reappear on a page refresh.
//
// The fix is not a clear-the-registry route. It is that both sides now name the
// same instant: the press event carries the registry's own `pressed_at`, the
// browser stores THAT as the mark, and dismissing it records the value
// dismissed. A hydrated mark carrying a value already dismissed is the one this
// browser threw away; a NEWER one is a new press and must show.
//
// Kept as a pure module so a test can import it without the store graph — and
// because the comparison is the whole feature.

// shouldHydrateMark decides whether a mark replayed by the platform should
// appear in this browser.
export const shouldHydrateMark = (
  dismissedAt: number | undefined,
  pressedAt: number,
): boolean => {
  if (dismissedAt === undefined) return true
  // Strictly newer. Equal means it is the same press, already dismissed here.
  return pressedAt > dismissedAt
}

// markTimestamp reads the mark instant off a press event, falling back to the
// local clock for a backend that does not send one yet.
//
// The fallback is honest but weaker: a locally-timed mark cannot be compared
// with a hydrated one, so dismissing it does not survive a reconnect — which is
// exactly the behaviour this replaces, and only for an older backend.
export const markTimestamp = (
  pressedAt: string | undefined,
  now: number,
): number => {
  if (!pressedAt) return now
  const parsed = new Date(pressedAt).getTime()
  return Number.isFinite(parsed) ? parsed : now
}
