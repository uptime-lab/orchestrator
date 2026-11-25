import { createEffect, createStore } from 'effector'

import { UserUpdatePayload } from '@/entities/user/api/putUpdateUser.ts'
import { UserEntity, UserRole } from '@/entities/user/api/types'

import { deriveById } from '@/shared/utils/mergeUniqueEntities'

import { apiUsers } from './api'
import { sortUsers } from './utils'

// ============= Store =============

export const $users = createStore<UserEntity[]>([])
export const $usersById = $users.map(deriveById)
export const $hasRequestedUsers = $users.map((users) =>
  users.some((user) => user.role === UserRole.Request),
)
export const $usersTotalCount = $users.map((users) => users.length)

// ============= Effects =============

export const fetchUsersFx = createEffect(() => apiUsers.getUsers())

export const updateUserFx = createEffect(
  async (payload: {
    user_id: string
    payload: UserUpdatePayload
  }): Promise<Pick<UserEntity, 'id'> & Partial<UserEntity>> => {
    await apiUsers.putUpdateUser(payload)
    const userUpdate = { id: payload.user_id, ...payload.payload }
    userUpdate.password = undefined
    return userUpdate
  },
)

/**
 * Approve user request and update their role to a default one
 */
export const approveUserFx = createEffect(
  async (payload: { user_id: string }) => {
    await apiUsers.putApproveUser(payload)
  },
)

export const deleteUserFx = createEffect(
  async (payload: { user_id: string }) => {
    await apiUsers.deleteUser(payload)
  },
)

// ============= Side effects =============

$users
  .on(fetchUsersFx.doneData, (_, users) => [...users].sort(sortUsers))
  .on(updateUserFx.doneData, (state, updatedUser) =>
    state.map((user) =>
      user.id === updatedUser.id ? { ...user, ...updatedUser } : user,
    ),
  )
  .on(approveUserFx.done, (state, updatedUser) =>
    state.map((user) =>
      // todo: the real role should be returned from BE, that is a tricky part
      // alternatively, we can re-fetch the user, but that is not ideal as well
      user.id === updatedUser.params.user_id
        ? { ...user, role: UserRole.Normal, is_active: true }
        : user,
    ),
  )
  .on(deleteUserFx.done, (state, { params }) =>
    state.filter((user) => user.id !== params.user_id),
  )
