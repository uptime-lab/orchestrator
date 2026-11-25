import { createEvent, createStore } from 'effector'
import { t } from 'i18next'

// The web half of an identify blink: while the device's locate LED blinks,
// the node's ID lamp in the list blinks too. Fed from every place a blink can
// start — the list lamp, the panel's identify button, and (via buttonPulse's
// consumers) the physical button — so the lamp does not depend on which
// control started it. Transient by design; nothing is persisted, and the web
// timer is an honest approximation of the device timer, not a report from it.

// Legacy fallback only — the effective duration is an ACCOUNT SETTING now
// (default 300 s) and rides the identify response / the button WS event, so
// this constant is used only when neither carries one (an older backend).
export const IDENTIFY_BLINK_MS = 30_000

// formatBlink renders a duration for toasts: whole minutes as minutes,
// everything else as seconds.
export const formatBlink = (seconds: number): string => {
  if (seconds >= 60 && seconds % 60 === 0) {
    return t('entities.node.identifyBlink.minutes', { count: seconds / 60 })
  }
  return t('entities.node.identifyBlink.seconds', { count: seconds })
}

// The agent clamps any identify shorter than this up to it (identifyMinSec in
// agent/pkg/application/handlers/identify_linux.go). There is no "stop
// identify" command — but the agent REPLACES a running blink with an incoming
// one, so the shortest legal identify is how a stop is spelled, and five
// seconds is how long the LED keeps going after the operator asked it not to.
export const IDENTIFY_MIN_SEC = 5

export const identifyBlinkStarted = createEvent<{
  nodeId: string
  durationMs: number
}>()
// Explicit stop, unlike the timer-driven clear below: it drops the node
// whatever its stored end time says.
export const identifyBlinkStopped = createEvent<string>()
const blinkCleared = createEvent<string>()

// Node ids currently blinking -> blink end timestamp. A repeat identify
// extends the blink; the clear compares against the stored end, so an older
// timer cannot cut a newer blink short.
export const $identifyBlinkNodes = createStore<Record<string, number>>({})
  .on(identifyBlinkStarted, (state, { nodeId, durationMs }) => ({
    ...state,
    [nodeId]: Date.now() + durationMs,
  }))
  .on(identifyBlinkStopped, (state, id) => {
    if (!(id in state)) return state
    const next = { ...state }
    delete next[id]
    return next
  })
  .on(blinkCleared, (state, id) => {
    const until = state[id]
    if (until === undefined || Date.now() < until - 50) {
      return state
    }
    const next = { ...state }
    delete next[id]
    return next
  })

identifyBlinkStarted.watch(({ nodeId, durationMs }) => {
  setTimeout(() => blinkCleared(nodeId), durationMs)
})

// hydrateIdentifyState pulls the platform's transient identify/button
// registry and replays it into the local stores — the walk-to-the-PC case: a
// press at the rack must be findable by a page opened AFTER it. Called on
// socket connect (the moment the app knows it is authed and live).
export const hydrateIdentifyState = async () => {
  const [{ fetchIdentifyStates }, { buttonMarkHydrated }] = await Promise.all([
    import('@/entities/deviceProfile/api'),
    import('./buttonPulse'),
  ])
  try {
    const states = await fetchIdentifyStates()
    for (const st of states) {
      if (st.blink_until) {
        const remaining = new Date(st.blink_until).getTime() - Date.now()
        if (remaining > 500) {
          identifyBlinkStarted({ nodeId: st.node_id, durationMs: remaining })
        }
      }
      if (st.pressed_at) {
        buttonMarkHydrated({
          nodeId: st.node_id,
          pressedAt: new Date(st.pressed_at).getTime(),
        })
      }
    }
  } catch {
    // Cosmetic state: a failed hydration must never break the page.
  }
}
