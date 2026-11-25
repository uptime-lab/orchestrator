import clsx from 'clsx'
import { createLocator, Locator, Mark } from 'create-locator'
import { FC } from 'react'
import { useTranslation } from 'react-i18next'

import { authHooks } from '@/entities/auth/index.ts'

import { useUsers } from '../../hooks.ts'
import { UsersTableRow, UsersTableRowLocator } from './UsersTableRow.tsx'

export type UsersTableLocator = Locator<{
  user: UsersTableRowLocator
}>

export type UsersTableProps = {} & Mark<UsersTableLocator>

export const UsersTable: FC<UsersTableProps> = ({
  ...rest
}: UsersTableProps) => {
  const { t } = useTranslation()
  const locator = createLocator(rest)

  const currentUserId = authHooks.useCurrentUserId()
  const { users } = useUsers()

  return (
    <>
      <div className="w-full" {...locator()}>
        <div
          className={clsx(
            'sticky top-0 hidden md:flex h-[51px] items-center px-2 gap-3',
            'text-sm font-medium uppercase text-neutral-7',
          )}
        >
          <div className="w-col-md">{t('entities.user.table.header.user')}</div>
          <div className="flex-1">
            {t('entities.user.table.header.profile')}
          </div>
          <div className="flex-1 hidden xl:block">
            {t('entities.user.table.header.lastVisit')}
          </div>
          <div className="flex-1 hidden xl:block">
            {t('entities.user.table.header.registered')}
          </div>
          <div className="w-col-actions" />
        </div>
        <div className="flex flex-col gap-1.5">
          {users.map((entity) => (
            <UsersTableRow
              key={entity.id}
              entity={entity}
              isYou={entity.id === currentUserId}
              {...locator.user()}
            />
          ))}
        </div>
      </div>
    </>
  )
}
