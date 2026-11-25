// The bridge between "a request was refused pending confirmation" and "the user
// confirmed", so the axios interceptor can await a dialog it must not import.
//
// Deliberately a promise rather than effector state: the interceptor needs to
// SUSPEND one request until an answer arrives and then resume it, which reads
// naturally as an await and awkwardly as a store subscription. Only one host is
// ever mounted, so this stays a single subscriber rather than an event bus.

type Resolver = (confirmed: boolean) => void

let host: ((resolve: Resolver) => void) | null = null

// Requests that arrived while a prompt was already open. They all wait on the
// SAME answer — three gated calls in flight must not stack three dialogs.
let waiting: Resolver[] = []
let open = false

// subscribeToReauthPrompt registers the dialog host. Returns an unsubscribe.
export const subscribeToReauthPrompt = (
  handler: (resolve: Resolver) => void,
): (() => void) => {
  host = handler
  return () => {
    host = null
  }
}

// requestReauth asks the user to confirm and resolves with whether they did.
// Resolves false when no host is mounted — a caller must never hang forever
// waiting for a dialog that cannot appear.
export const requestReauth = (): Promise<boolean> => {
  if (!host) {
    return Promise.resolve(false)
  }
  return new Promise<boolean>((resolve) => {
    waiting.push(resolve)
    if (open) {
      return
    }
    open = true
    host?.((confirmed) => {
      open = false
      const pending = waiting
      waiting = []
      pending.forEach((r) => r(confirmed))
    })
  })
}
