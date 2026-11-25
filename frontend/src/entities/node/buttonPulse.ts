import { createEvent, createStore } from 'effector'
import { t } from 'i18next'

import { showSuccess } from '@/shared/ui/toast'

import {
  $identifyBlinkNodes,
  formatBlink,
  IDENTIFY_BLINK_MS,
  identifyBlinkStarted,
  identifyBlinkStopped,
} from './identifyBlink'
import { markTimestamp, shouldHydrateMark } from './markDismissal'

// The device-to-web half of identify: a human pressed the physical button on
// a node, and the UI must light that node up. The press TOGGLES a MARK rather
// than firing a short pulse — the person who pressed it is standing at the
// rack, and a mark that has expired by the time they reach a screen has told
// them nothing. A second press clears it. The event is fed by the user socket
// (entities/userCommonSocket); the store lives HERE so the node list can
// consume it without an entities-level import cycle.
//
// Two consequences of the toggle being PARITY over a broadcast with no
// server-side state, both accepted deliberately: a browser that was not open
// for the first press reads the second one as "mark" (harmless — the mark is
// only a highlight, and the operator is looking at the rack), and a mark left
// standing would otherwise live for the whole session, hence the backstop
// below. The backstop is a leak guard, NOT a UX timer — do not shorten it into
// one, that is the pulse this replaced.

export type NodeButtonPress = {
  node_id: string
  node_name: string
  press_kind: string
  // When the device started a blink, the platform reports WHEN it will end
  // (RFC3339) — computed from the account's identify duration. The web lamp
  // runs to exactly that moment, so device and UI never desync.
  blink_until?: string
  // ...and how long the blink IS, which is a different question from how much
  // of it is left by the time this browser reads the event. Subtracting the
  // end time from a local clock loses the flight time (a 300-second setting
  // arrives as 299) and any clock skew on top of it, so the number the
  // operator is told comes from here and only the fallback computes it.
  duration_sec?: string
  // The registry's own instant for a press that SET a mark (RFC3339). Stored as
  // the mark's value so this browser and the platform name the same press —
  // which is what lets a dismissal be keyed by value instead of by two clocks.
  pressed_at?: string
}

export const nodeButtonPressed = createEvent<NodeButtonPress>()
// Server-side hydration: a press recorded by the platform's registry before
// this page existed (the event carries when it happened, so the backstop
// counts from the PRESS, not from the page load).
export const buttonMarkHydrated = createEvent<{
  nodeId: string
  pressedAt: number
}>()
// Clear the mark from the web side (the ID button's own off switch). LOCAL
// only when nothing is blinking: that click deliberately dispatches nothing to
// the device, so it reaches no endpoint and the registry keeps its `pressed_at`
// until the TTL runs out. What stops the next reconnect hydrating it back is
// $dismissedMarks below — the dismissal remembers WHICH press it threw away, so
// a replay of that same press is suppressed while a newer one still shows. A
// click that DOES stop a blink propagates: the identify stop clears both halves
// of the registry entry.
export const buttonMarkCleared = createEvent<string>()
const markCleared = createEvent<{ nodeId: string; markedAt: number }>()

const markBackstopMs = 10 * 60 * 1000

// Marks this browser has dismissed: node id -> the press instant thrown away.
//
// It has to outlive the mark store, because the point is to recognise a mark
// the platform replays AFTER the local one is gone. Bounded by the same fact
// that bounds the registry: a newer press overwrites the entry, and the
// registry stops replaying anything older than its TTL, so a stale entry
// suppresses nothing that could still arrive.
// DECLARED BEFORE $buttonPressedNodes on purpose. Effector runs subscribers in
// registration order, so this handler reads the mark while it is still there;
// declared after, it would read a store the same event has already emptied and
// remember nothing.
export const $dismissedMarks = createStore<Record<string, number>>({})
  .on(buttonMarkCleared, (state, nodeId) => {
    const markedAt = $buttonPressedNodes.getState()[nodeId]
    if (markedAt === undefined) return state
    return { ...state, [nodeId]: markedAt }
  })
  .on(nodeButtonPressed, (state, p) => {
    // A new press supersedes any dismissal: the mark value moves forward and
    // the old entry would only suppress a press that can no longer arrive.
    if (!(p.node_id in state)) return state
    const next = { ...state }
    delete next[p.node_id]
    return next
  })

// Node ids currently MARKED by a physical press -> when they were marked.
export const $buttonPressedNodes = createStore<Record<string, number>>({})
  .on(nodeButtonPressed, (state, p) => {
    // Since the agent reports the press OUTCOME (blink_started /
    // blink_stopped), the mark store stays out of it entirely: the blink
    // store mirrors the device, and parity-toggling here is what inverted
    // against reality after any page refresh (in both directions). Only the
    // legacy "short" kind — an old agent, or a blink
    // that failed to start — keeps the mark-toggle behaviour.
    if (p.press_kind === 'blink_started' || p.press_kind === 'blink_stopped') {
      if (p.node_id in state) {
        const next = { ...state }
        delete next[p.node_id]
        return next
      }
      return state
    }
    // Legacy: a press while this browser shows the node blinking is the
    // acknowledgement, not a mark request.
    if ($identifyBlinkNodes.getState()[p.node_id] !== undefined) {
      return state
    }
    const next = { ...state }
    if (p.node_id in next) {
      delete next[p.node_id]
      return next
    }
    // The platform's instant when it sends one, so this value and the hydrated
    // one are the same number and a dismissal can be compared against it.
    next[p.node_id] = markTimestamp(p.pressed_at, Date.now())
    return next
  })
  .on(buttonMarkHydrated, (state, { nodeId, pressedAt }) => {
    if (nodeId in state) return state
    if (!shouldHydrateMark($dismissedMarks.getState()[nodeId], pressedAt)) {
      return state
    }
    return { ...state, [nodeId]: pressedAt }
  })
  .on(buttonMarkCleared, (state, nodeId) => {
    if (!(nodeId in state)) return state
    const next = { ...state }
    delete next[nodeId]
    return next
  })
  // The backstop fires against the mark it was armed for: toggling a node off
  // and on again inside the window must not be cut short by the older timer.
  .on(markCleared, (state, { nodeId, markedAt }) => {
    if (state[nodeId] !== markedAt) return state
    const next = { ...state }
    delete next[nodeId]
    return next
  })

nodeButtonPressed.watch((p) => {
  const name = p.node_name || t('entities.node.buttonPulse.fallbackName')
  // Outcome-carrying kinds mirror the device exactly (local state only; no
  // browser sends a command on the device's behalf).
  if (p.press_kind === 'blink_started') {
    // The account's effective duration, as sent. Falling back through
    // blink_until (an older backend) and then the constant (older still):
    // both are approximations of a number the platform knows exactly, so
    // neither is used while the real one is on the event.
    const sent = Number(p.duration_sec)
    const untilMs = p.blink_until ? new Date(p.blink_until).getTime() : NaN
    const durationMs =
      Number.isFinite(sent) && sent > 0
        ? sent * 1000
        : Number.isFinite(untilMs) && untilMs > Date.now()
          ? untilMs - Date.now()
          : IDENTIFY_BLINK_MS
    identifyBlinkStarted({ nodeId: p.node_id, durationMs })
    showSuccess(
      t('entities.node.buttonPulse.blinkStarted', {
        name,
        duration: formatBlink(Math.round(durationMs / 1000)),
      }),
    )
    return
  }
  if (p.press_kind === 'blink_stopped') {
    identifyBlinkStopped(p.node_id)
    showSuccess(t('entities.node.buttonPulse.identifyStopped', { name }))
    return
  }
  // Legacy acknowledgement case: the node was blinking, the human pressed
  // its button, the device's LED already stopped — stop the web lamp too.
  if ($identifyBlinkNodes.getState()[p.node_id] !== undefined) {
    identifyBlinkStopped(p.node_id)
    showSuccess(t('entities.node.buttonPulse.identifyAcknowledged', { name }))
    return
  }
  // The store's .on above is registered first, so by the time this runs the
  // toggle has been applied and the state IS the outcome to report.
  const markedAt = $buttonPressedNodes.getState()[p.node_id]
  showSuccess(
    markedAt !== undefined
      ? t('entities.node.buttonPulse.marked', { name })
      : t('entities.node.buttonPulse.markCleared', { name }),
  )
  if (markedAt === undefined) {
    // The press that clears the mark clears the whole lit state, including a
    // platform-started blink — otherwise "press it again to turn it off"
    // leaves the row still lit whenever an operator had also clicked ID. The
    // device's own LED is not ours to stop from here (several browsers see
    // this event and none of them should send a command on the device's
    // behalf); a running platform blink simply expires within 30 s.
    identifyBlinkStopped(p.node_id)
    return
  }
  setTimeout(() => markCleared({ nodeId: p.node_id, markedAt }), markBackstopMs)
})

buttonMarkHydrated.watch(({ nodeId, pressedAt }) => {
  const remaining = markBackstopMs - (Date.now() - pressedAt)
  if (remaining > 0) {
    setTimeout(() => markCleared({ nodeId, markedAt: pressedAt }), remaining)
  }
})
