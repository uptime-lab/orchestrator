import { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { UserEntity, UserRole } from '@/entities/user/api/types'

import Badge, { BadgeColor, BadgeVariant } from '@/shared/ui/badge'

export type UserBadgeType =
  | 'invited'
  | 'requested'
  | 'active'
  | 'inactive'
  | 'admin'

const userBadgeToVisual: Record<
  UserBadgeType,
  { color: BadgeColor; variant: BadgeVariant; title: (t: TFunction) => string }
> = {
  invited: {
    color: 'neutral',
    variant: 'outline',
    title: (t) => t('entities.user.table.status.invited'),
  },
  requested: {
    color: 'yellow',
    variant: 'outline',
    title: (t) => t('entities.user.table.status.requested'),
  },
  active: {
    color: 'green',
    variant: 'solid',
    title: (t) => t('entities.user.table.status.active'),
  },
  inactive: {
    color: 'red',
    variant: 'solid',
    title: (t) => t('entities.user.table.status.inactive'),
  },
  admin: {
    color: 'violet',
    variant: 'solid',
    title: (t) => t('entities.user.table.status.admin'),
  },
}

const getUserStatus = (
  entity: Pick<UserEntity, 'role' | 'is_active'>,
): UserBadgeType => {
  if (entity.role === UserRole.Admin) {
    return 'admin'
  }
  if (entity.role === UserRole.Request) {
    return 'requested'
  }
  return entity.is_active ? 'active' : 'inactive'
}

type UserBadgeProps = {
  user: Pick<UserEntity, 'role' | 'is_active'>
}

export const UserBadge = ({ user }: UserBadgeProps) => {
  const { t } = useTranslation()
  const badge = userBadgeToVisual[getUserStatus(user)]

  return (
    <Badge color={badge.color} variant={badge.variant}>
      {badge.title(t)}
    </Badge>
  )
}
