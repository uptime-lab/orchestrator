import { MenuItem } from '@headlessui/react'
import { Link, LinkProps, useMatchRoute } from '@tanstack/react-router'
import clsx from 'clsx'
import { TFunction } from 'i18next'
import { forwardRef, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { authHooks, authModel } from '@/entities/auth'
import { getTermsUrl } from '@/entities/terms'
import { UserAvatar } from '@/entities/user/ui/userAvatar'
import { UserBadge } from '@/entities/user/ui/userBadge'
import { getFullName } from '@/entities/user/utils.ts'

import { IconCog, IconUsers } from '@/shared/assets/icons'

import { OrgSwitcher } from './OrgSwitcher.tsx'

type MenuLink = {
  link: LinkProps['to']
  label: (t: TFunction) => string
  icon: ReactNode
}

const links: MenuLink[] = [
  {
    link: '/team',
    label: (t) => t('pages.team.title'),
    icon: <IconUsers className="w-3.5" />,
  },
  {
    link: '/account',
    label: (t) => t('routes.account.title'),
    icon: <IconCog className="w-3.5" />,
  },
]

export const AccountMenu = forwardRef<HTMLDivElement>((_, ref) => {
  const { t } = useTranslation()
  const matchRoute = useMatchRoute()

  const user = authHooks.useCurrentUser()

  if (!user) {
    return null
  }

  const termsUrl = getTermsUrl()

  return (
    <div className="pt-3 rounded bg-neutral-12 w-[292px]" ref={ref}>
      <div className="flex gap-2 items-center py-1 px-4">
        <UserAvatar user={user} size="xs" />
        <div className="min-w-0">
          <div className="flex items-center gap-2 mb-0.5">
            <div className="text-sm leading-tight text-neutral-5 truncate">
              {getFullName(user)}
            </div>
            <UserBadge user={user} />
          </div>
          <div className="text-xs text-neutral-7 leading-none truncate">
            {user.email}
          </div>
        </div>
      </div>
      <div className="flex flex-col gap-1 py-2">
        {links.map((link) => (
          <MenuItem key={link.link}>
            <Link
              to={link.link}
              className={clsx(
                'min-h-8 px-4 flex gap-2 items-center',
                'text-sm',
                matchRoute({ to: link.link })
                  ? 'text-lime-6 bg-neutral-11 data-[focus]:bg-neutral-10'
                  : 'text-neutral-6 data-[focus]:bg-neutral-11',
              )}
            >
              {link.icon} {link.label(t)}
            </Link>
          </MenuItem>
        ))}
      </div>
      {/*
        The operator's terms of use, shown only where TERMS_URL is set. It
        leaves the app, so it is an <a> and not a router Link, and it is marked
        as leaving, since a menu item that silently opens a new tab reads as a
        bug the first time.
      */}
      {termsUrl && (
        <div className="border-t border-neutral-11 py-1">
          <MenuItem>
            <a
              href={termsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="min-h-8 px-4 flex gap-2 items-center text-sm text-neutral-6 data-[focus]:bg-neutral-11"
            >
              <svg
                className="w-3.5"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth={2}
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
                <polyline points="14 2 14 8 20 8" />
              </svg>
              {t('widgets.accountMenu.terms')}
              <span aria-hidden className="ml-auto text-xs text-neutral-7">
                ↗
              </span>
            </a>
          </MenuItem>
        </div>
      )}
      <OrgSwitcher />
      <div className="border-t border-neutral-11 py-1">
        <MenuItem>
          <button
            type="button"
            onClick={() => authModel.events.signOut()}
            className="w-full min-h-8 px-4 flex gap-2 items-center text-sm text-neutral-6 data-[focus]:bg-neutral-11"
          >
            <svg
              className="w-3.5"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth={2}
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
              <polyline points="16 17 21 12 16 7" />
              <line x1="21" y1="12" x2="9" y2="12" />
            </svg>
            {t('widgets.accountMenu.logout')}
          </button>
        </MenuItem>
      </div>
    </div>
  )
})
