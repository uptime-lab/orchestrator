import { deleteUser } from './deleteUser.ts'
import { getCurrentUser } from './getCurrentUser.ts'
import { getUsers } from './getUsers.ts'
import { putApproveUser } from './putApproveUser.ts'
import { putUpdateUser } from './putUpdateUser.ts'

export const apiUsers = {
  getUsers,
  getCurrentUser,
  putUpdateUser,
  putApproveUser,
  deleteUser,
}
