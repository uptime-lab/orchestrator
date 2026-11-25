import { useEffect, useState } from 'react'

import { loadSignInOptions } from '@/entities/signInOptions'

import { onceCache } from '@/shared/utils/onceCache'

import { getOidcConfig, OidcConfig } from '../api/oidc.ts'

type State = { loading: boolean; config: OidcConfig | null }

// fetchOidcConfig asks for the provider only where the install serves single
// sign-on, which the sign-in options say. Elsewhere /auth/oidc/config does not
// exist, and asking anyway puts a 404 in the server's log on every page load.
// Options that cannot be read, or a backend too old to report the field, fall
// back to asking.
const fetchOidcConfig = async (): Promise<OidcConfig | null> => {
  const options = await loadSignInOptions()
  if (options?.single_sign_on === false) return null
  return getOidcConfig()
}

// One request per page load: the sign-in, sign-up and account pages each
// mount several components that ask. The provider only moves with a backend
// restart.
const cache = onceCache(fetchOidcConfig)

export const loadOidcConfig = cache.load

// useOidcConfig fetches whether OIDC login is available (and its button label).
// While loading, config is null and loading is true so callers can avoid
// flicker. A disabled/absent provider yields config === null.
export const useOidcConfig = (): State => {
  const [state, setState] = useState<State>({ loading: true, config: null })

  useEffect(() => {
    let alive = true
    loadOidcConfig().then((config) => {
      if (alive)
        setState({ loading: false, config: config?.enabled ? config : null })
    })
    return () => {
      alive = false
    }
  }, [])

  return state
}
