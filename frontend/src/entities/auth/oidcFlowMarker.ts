// A handoff code arriving at /oidc-callback is only consumed by the browser tab
// that started the sign-in. Without this, a link to the callback carrying
// someone else's fresh code signs the visitor in as that someone — into an
// account where whatever they then enter or adopt is not theirs.
//
// sessionStorage, not localStorage: it belongs to the one tab that navigated
// to the identity provider and survives that round trip, and no other page or
// tab can set it for this one.

const MARKER_KEY = 'orchestrator_oidc_flow_started'

// Longer than any real round trip through an identity provider, including a
// password and a second factor typed there.
export const OIDC_FLOW_MAX_AGE_MS = 30 * 60 * 1000

type MarkerStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

const sessionStore = (): MarkerStorage | null => {
  try {
    return typeof sessionStorage !== 'undefined' ? sessionStorage : null
  } catch {
    return null
  }
}

export const markOidcFlowStarted = (
  storage: MarkerStorage | null = sessionStore(),
  now: () => number = Date.now,
): void => {
  try {
    storage?.setItem(MARKER_KEY, String(now()))
  } catch {
    // Without storage the callback refuses; the user is told to start over.
  }
}

// takeOidcFlowMarker reports whether this tab started a sign-in recently, and
// clears the marker either way: one flow, one code.
export const takeOidcFlowMarker = (
  storage: MarkerStorage | null = sessionStore(),
  now: () => number = Date.now,
): boolean => {
  if (!storage) return false
  try {
    const raw = storage.getItem(MARKER_KEY)
    storage.removeItem(MARKER_KEY)
    if (!raw) return false
    const startedAt = Number(raw)
    if (!Number.isFinite(startedAt)) return false
    const age = now() - startedAt
    return age >= 0 && age <= OIDC_FLOW_MAX_AGE_MS
  } catch {
    return false
  }
}

// The verdict per code, so a component that mounts twice (development's
// strict mode, a remount) asks once and gets the same answer.
const verdicts = new Map<string, boolean>()

export const oidcCodeStartedHere = (
  code: string,
  storage: MarkerStorage | null = sessionStore(),
  now: () => number = Date.now,
): boolean => {
  const known = verdicts.get(code)
  if (known !== undefined) return known
  const verdict = takeOidcFlowMarker(storage, now)
  verdicts.set(code, verdict)
  return verdict
}
