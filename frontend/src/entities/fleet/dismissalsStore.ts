import { useSyncExternalStore } from 'react'

import { readDismissals, writeDismissals, type Dismissals } from './dismissals'

// A reactive view over the dismissals in localStorage.
//
// It exists because two components need the same answer and only one of them
// changes it: the Status page hides a condition, and the nav badge — a
// different tree entirely — has to stop counting it. localStorage fires no
// event for the tab that wrote it, so without this the header kept the old
// number until something else re-rendered it.
//
// useSyncExternalStore rather than an Effector store: the state is one object
// that lives in the browser, not application state the app graph reasons
// about, and keeping it out of the store graph is what lets dismissals.ts stay
// pure enough to import in a node-environment test.

let snapshot: Dismissals = readDismissals()
const listeners = new Set<() => void>()

const subscribe = (listener: () => void) => {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

const getSnapshot = () => snapshot

/** Replaces the set, persists it, and wakes every reader. */
export const setDismissals = (next: Dismissals) => {
  snapshot = next
  writeDismissals(next)
  listeners.forEach((listener) => listener())
}

export const useDismissals = (): Dismissals =>
  useSyncExternalStore(subscribe, getSnapshot, getSnapshot)
