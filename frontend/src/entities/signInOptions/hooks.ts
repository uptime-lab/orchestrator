import { useEffect, useState } from 'react'

import { onceCache } from '@/shared/utils/onceCache'

import { getSignInOptions, SignInOptions } from './api'

type State = { loading: boolean; options: SignInOptions | null }

// One request per page: several components on one page ask for it. The
// registration state moves when the first account is created, so the
// sign-up route forgets the kept answer on every visit
// (forgetSignInOptions) rather than trusting it for the life of the tab.
const cache = onceCache(getSignInOptions)

export const loadSignInOptions = cache.load
export const forgetSignInOptions = cache.forget

// useSignInOptions returns the install's mail and registration signals.
// While loading, and when they cannot be read, options is null and callers
// keep their previous behaviour.
export const useSignInOptions = (): State => {
  const [state, setState] = useState<State>({ loading: true, options: null })

  useEffect(() => {
    let alive = true
    loadSignInOptions().then((options) => {
      if (alive) setState({ loading: false, options })
    })
    return () => {
      alive = false
    }
  }, [])

  return state
}
