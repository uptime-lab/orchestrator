// The terminal panel's view state, kept pure so the transitions are testable.
//
// A refused session (expired sign-in, revoked access, a device that never
// answers) ENDS the session but keeps the view open with the message and a
// retry: in the embedded sub-view, leaving the view unmounts the panel, and the
// message would vanish with it. Only an explicit close leaves the view.
export type TerminalView = {
  open: boolean
  // The session was refused or failed before producing output; the socket and
  // the xterm instance are torn down, the message stays on screen.
  refused: boolean
  error: string | null
  // Bumped on retry so the session effect re-runs with a fresh socket.
  attempt: number
}

export type TerminalViewEvent =
  | { type: 'open' }
  | { type: 'refuse'; message: string }
  | { type: 'retry' }
  | { type: 'close' }

export const initialTerminalView = (embedded: boolean): TerminalView => ({
  open: embedded,
  refused: false,
  error: null,
  attempt: 0,
})

export const terminalViewReducer = (
  state: TerminalView,
  event: TerminalViewEvent,
): TerminalView => {
  switch (event.type) {
    case 'open':
      return { ...state, open: true, refused: false, error: null }
    case 'refuse':
      if (!state.open) return state
      return { ...state, refused: true, error: event.message }
    case 'retry':
      return {
        open: true,
        refused: false,
        error: null,
        attempt: state.attempt + 1,
      }
    case 'close':
      return { ...state, open: false, refused: false, error: null }
  }
}

// Whether a live session (socket + xterm) should exist for this view state.
export const sessionWanted = (state: TerminalView): boolean =>
  state.open && !state.refused
