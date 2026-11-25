import { useNavigate } from '@tanstack/react-router'
import { useStoreMap, useUnit } from 'effector-react'
import { useEffect } from 'react'

import { authToken } from '@/shared/utils/apiConfig.ts'

import {
  $authState,
  $loginChallenge,
  $twoFactorChallenge,
  events,
  initAuthFx,
  signUpAndLoginAuthFx,
} from './model'
import { AuthStatus } from './types'

export const useAuthInit = () => {
  const state = useUnit($authState)

  useEffect(() => {
    if (state.status === AuthStatus.Idle) {
      const accessToken = authToken.getAccessToken()
      const refreshToken = authToken.getRefreshToken()

      // If we have any token, try to initialize auth
      if (accessToken || refreshToken) {
        initAuthFx()
      } else {
        // No tokens, user is logged out
        events.signOut()
      }
    }
  }, [state.status])

  return { state }
}

export const useAuthState = () => {
  return useStoreMap($authState, (state) => state)
}

export const useAuthStatus = () => {
  return useStoreMap($authState, (state) => state.status)
}

export const useCurrentUserId = () => {
  return useStoreMap($authState, (state) => {
    if (state.status !== AuthStatus.LoggedIn) {
      return undefined
    }
    return state.user.id
  })
}

export const useCurrentUser = () => {
  return useStoreMap($authState, (state) => {
    if (state.status !== AuthStatus.LoggedIn) {
      return undefined
    }
    return state.user
  })
}

// useLoginChallenge exposes the pending account-selection challenge (null
// when none).
export const useLoginChallenge = () => {
  return useUnit($loginChallenge)
}

// useTwoFactorChallenge exposes the pending second-factor ticket (null when
// none). Separate from the account challenge: both can occur in one login, in
// that order.
export const useTwoFactorChallenge = () => {
  return useUnit($twoFactorChallenge)
}

export const useAuthOnlyRoute = () => {
  const navigate = useNavigate()
  const authState = useAuthState()

  useEffect(() => {
    if (authState.status !== AuthStatus.LoggedIn) {
      navigate({ to: '/login', search: { redirect: location.pathname } })
    }
  }, [authState.status, navigate])

  return { authState }
}

export const useIsSignUpPending = () => {
  return useUnit(signUpAndLoginAuthFx.pending)
}
