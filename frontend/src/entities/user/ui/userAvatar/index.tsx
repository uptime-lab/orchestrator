import clsx from 'clsx'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { colorSystem } from '@root/tailwind.config.ts'

import type { UserEntity } from '@/entities/user/api/types'
import { getInitials } from '@/entities/user/utils.ts'

import { getUuidHash } from '@/shared/utils/uuid.ts'

type UserAvatarSize = 'xxs' | 'xs' | 'sm' | 'md'
type UserAvatarColors = keyof typeof colorSystem

type UserAvatarProps = {
  user: Pick<UserEntity, 'id' | 'name' | 'surname' | 'email'>
  size?: UserAvatarSize
  bordered?: boolean
}

const sizeClasses: Record<UserAvatarSize, string> = {
  xxs: 'h-6 w-6 text-xs',
  xs: 'h-8 w-8 text-xs',
  sm: 'h-10 w-10',
  md: 'h-12 w-12',
}

// Text color per background: the saturated -7/-8 shades of red, pink,
// purple, violet and blue are too dark for text-neutral-15 to reach WCAG AA
// (violet-8 is 2.28:1), so those use near-white; keep any new pair above
// 4.5:1. The initials span below inherits this color — do not set one there.
const colorClasses: Record<UserAvatarColors, string> = {
  neutral: 'bg-neutral-5 text-neutral-15',
  red: 'bg-red-7 text-neutral-1',
  pink: 'bg-pink-7 text-neutral-1',
  purple: 'bg-purple-8 text-neutral-1',
  violet: 'bg-violet-8 text-neutral-1',
  blue: 'bg-blue-8 text-neutral-1',
  lightBlue: 'bg-lightBlue-8 text-neutral-15',
  green: 'bg-green-8 text-neutral-15',
  lime: 'bg-lime-8 text-neutral-15',
  yellow: 'bg-yellow-8 text-neutral-15',
  amber: 'bg-amber-8 text-neutral-15',
  orange: 'bg-orange-8 text-neutral-15',
}
const colorClassesKeys = Object.keys(colorClasses) as UserAvatarColors[]

export const UserAvatar = ({
  user,
  size = 'md',
  bordered,
}: UserAvatarProps) => {
  const { t } = useTranslation()
  const initials = getInitials(user)
  const color = useMemo<UserAvatarColors>(
    () =>
      colorClassesKeys[getUuidHash(user.id, colorClassesKeys.length)] || 'pink',
    [user.id],
  )

  return (
    <div
      className={clsx(
        'relative flex-none select-none items-center justify-center overflow-hidden rounded-full',
        'flex items-center justify-center',
        bordered && 'border-[1px] border-white',
        colorClasses[color] || colorClasses.blue,
        sizeClasses[size] || sizeClasses.md,
      )}
    >
      <span className="select-none font-medium">
        {initials || t('entities.user.avatar.blank')}
      </span>
    </div>
  )
}
