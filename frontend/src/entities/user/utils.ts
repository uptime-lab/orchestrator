import type { UserEntity } from '@/entities/user/api/types'

export const getFullName = (
  entity: Pick<UserEntity, 'name' | 'surname' | 'email'>,
) => {
  const fullName = [entity.name, entity.surname].filter(Boolean).join(' ')
  return fullName || entity.email
}

export const getInitials = (
  entity: Pick<UserEntity, 'name' | 'surname' | 'email'>,
) => {
  const initials = [entity.name, entity.surname]
    .map((el) => el.slice(0, 1))
    .join('')
    .toUpperCase()
  return initials || entity.email.slice(0, 2).toUpperCase()
}

export const sortUsers = (a: UserEntity, b: UserEntity) =>
  a.registered_at > b.registered_at ? 1 : -1
