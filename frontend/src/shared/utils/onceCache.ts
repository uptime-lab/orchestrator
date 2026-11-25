// onceCache shares one request between every component that asks for the
// same tokenless install signal on a page. A null answer (a failed read) is
// not kept, so the next caller asks again. forget() drops the kept answer,
// for a page whose wording depends on a value that may have moved since the
// tab opened.
export type OnceCache<T> = {
  load: () => Promise<T | null>
  forget: () => void
}

export const onceCache = <T>(fetch: () => Promise<T | null>): OnceCache<T> => {
  let inflight: Promise<T | null> | null = null
  return {
    load: () => {
      if (!inflight) {
        const p = fetch().then((value) => {
          if (value === null && inflight === p) inflight = null
          return value
        })
        inflight = p
      }
      return inflight
    },
    forget: () => {
      inflight = null
    },
  }
}
