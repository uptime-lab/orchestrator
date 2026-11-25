// Builds a whole-value write from the server's state at the moment of
// writing, not from a copy the page loaded earlier.
//
// Several routes replace a whole set (a member's role bindings, a user's
// units, a playbook's scope). A request computed from a cached copy re-sends
// every value in that copy, so a change someone else saved in between is
// silently undone. Reading first narrows that window to the round trip.
//
// `build` returns null when, against the current state, there is nothing to
// write (the change is already there, or its subject is gone); the write is
// then skipped and null returned.
export const writeFromCurrent = async <State, Request, Result>(
  read: () => Promise<State>,
  build: (current: State) => Request | null,
  write: (request: Request) => Promise<Result>,
): Promise<Result | null> => {
  const request = build(await read())
  if (request === null) return null
  return write(request)
}
