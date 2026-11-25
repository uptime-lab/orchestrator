import { UserEntity } from '../user/api/types'

export enum AuthStatus {
  Idle = 'idle',
  LoggedOut = 'loggedOut',
  LoggedIn = 'loggedIn',
  Locked = 'locked',
}

export type AuthStateIdle = { status: AuthStatus.Idle }
export type AuthStateOut = { status: AuthStatus.LoggedOut }
export type AuthStateIn = {
  status: AuthStatus.LoggedIn | AuthStatus.Locked
  user: UserEntity
}
export type AuthState = AuthStateIdle | AuthStateOut | AuthStateIn

export type SignInSuccess = { user: UserEntity }
