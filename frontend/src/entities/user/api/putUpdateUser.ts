import { apiClient } from '@/shared/utils/apiConfig'

import { UserEntity } from './types'

export type UserUpdatePayload = Pick<
  UserEntity,
  'name' | 'surname' | 'email' | 'is_active'
> & {
  password?: string
}

export const putUpdateUser = async ({
  user_id,
  payload,
}: {
  user_id: string
  payload: UserUpdatePayload
}) => {
  await apiClient.put(`/user/${user_id}`, payload)
  return 'ok'
}
