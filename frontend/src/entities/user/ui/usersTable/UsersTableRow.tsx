import clsx from 'clsx'
import { createLocator, type Locator, type Mark } from 'create-locator'
import { useTranslation } from 'react-i18next'

import type { UserEntity } from '@/entities/user/api/types'
import { UserAvatar } from '@/entities/user/ui/userAvatar'
import { UserBadge } from '@/entities/user/ui/userBadge'
import { getFullName } from '@/entities/user/utils.ts'

import { getDateFromSeconds } from '@/shared/utils/dateFormat.ts'

export type UsersTableRowLocator = Locator<{
  userId: { 'user-id': string }
}>

type UsersTableRowProps = {
  entity: UserEntity
  isYou?: boolean
} & Mark<UsersTableRowLocator>

export const UsersTableRow = ({
  entity,
  isYou,
  ...rest
}: UsersTableRowProps) => {
  const { t } = useTranslation()
  const locator = createLocator(rest)

  return (
    <div
      className={clsx(
        'flex items-center rounded flex-wrap md:flex-nowrap gap-x-3',
        'py-3 md:py-1.5 px-2 md:min-h-[51px]',
        'text-sm text-neutral-4 bg-neutral-14 hover:bg-neutral-13',
        'transition-all duration-100 ease-in',
      )}
      {...locator()}
    >
      <div
        {...locator.userId({ 'user-id': entity.id })}
        className="flex md:w-col-md items-center gap-3 text-white flex-1 md:flex-none"
      >
        <UserAvatar user={entity} size="xxs" />
        <div className="min-w-0 truncate whitespace-normal">
          {getFullName(entity)}
          {isYou && <>&nbsp;({t('entities.user.you')})</>}
        </div>
      </div>
      <div className="md:flex-1">
        <UserBadge user={entity} />
      </div>
      <div className="flex-1 truncate hidden xl:block">
        {entity.last_visited_at
          ? getDateFromSeconds(entity.last_visited_at).toLocaleString(
              undefined,
              {
                dateStyle: 'medium',
                timeStyle: 'short',
              },
            )
          : t('entities.user.table.blank')}
      </div>
      <div className="flex-1 truncate hidden xl:block">
        {entity.registered_at
          ? getDateFromSeconds(entity.registered_at).toLocaleString(undefined, {
              dateStyle: 'medium',
              timeStyle: 'short',
            })
          : t('entities.user.table.blank')}
      </div>
      <div className="flex w-full md:w-col-actions justify-end gap-2"></div>
    </div>
  )
}
