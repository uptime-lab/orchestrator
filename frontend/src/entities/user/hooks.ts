import { useStoreMap, useUnit } from 'effector-react'

import { UserEntity } from './api/types'
import {
  $hasRequestedUsers,
  $users,
  $usersById,
  $usersTotalCount,
} from './model'

export const useUsers = () => {
  const users = useUnit($users)
  const usersById = useUnit($usersById)

  return { users, usersById }
}

export const useUser = (payload: { userId?: string }) => {
  const user: UserEntity | null = useStoreMap({
    store: $usersById,
    keys: [payload.userId],
    fn: (usersById, [userId]) => (userId && usersById[userId]) || null,
  })
  return { user }
}

export const useHasRequestedUsers = () => {
  return useUnit($hasRequestedUsers)
}

export const useTotalUsersCount = () => {
  return useUnit($usersTotalCount)
}
