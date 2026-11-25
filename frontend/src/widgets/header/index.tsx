import { Menu, MenuButton, MenuItems } from '@headlessui/react'
import { useMatchRoute, useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { t } from 'i18next'
import { useState } from 'react'

import { AccountMenu } from '@/widgets/accountMenu'

import { authHooks } from '@/entities/auth'
import { useAttentionCount } from '@/entities/fleet'
import { Actions, useCanAnyScope } from '@/entities/permissions/hooks'
import { UserAvatar } from '@/entities/user/ui/userAvatar'

import { AssetLogo } from '@/shared/assets'
import { getConfigValue } from '@/shared/utils/config'

// Primary navigation, declared once and rendered in both the desktop bar and
// the mobile dropdown. Each entry carries the exact search params the section
// expects so deep-links (tabs) open in a sensible default state.
//
// `requires` is the READ action of the section behind the entry. It is checked
// in ANY scope, never against a target: a nav item asks "does this section
// exist for me", and the frontend's can() with no target demands an
// account-wide grant, so checking it that way would hide the entire nav from an
// OU-scoped member. The backend draws the same line for its catalog reads.
//
// Gating navigation is presentation, not enforcement — every route and every
// action behind it is gated server-side regardless.
type NavItem = {
  label: string
  to: string
  search: Record<string, unknown>
  isActive: (m: ReturnType<typeof useMatchRoute>) => boolean
  requires: string
  /** Renders the open-attention count beside the label. */
  showsAttentionCount?: boolean
}

const navItems = (): NavItem[] => [
  {
    label: t('widgets.header.nav.nodes'),
    to: '/',
    search: { sidebar: undefined },
    isActive: (m) => !!m({ to: '/' }),
    requires: Actions.NodeRead,
  },
  {
    label: t('widgets.header.nav.organization'),
    to: '/organization',
    search: { sidebar: undefined },
    isActive: (m) => !!m({ to: '/organization' }),
    requires: Actions.OURead,
  },
  {
    label: t('widgets.header.nav.credentials'),
    to: '/credentials',
    search: { sidebar: undefined, tab: 'ssh-keys' },
    isActive: (m) => !!m({ to: '/credentials' }) || !!m({ to: '/ssh-keys' }),
    requires: Actions.SSHKeyRead,
  },
  {
    label: t('widgets.header.nav.secrets'),
    to: '/secrets',
    search: { sidebar: undefined, tab: 'secrets' },
    isActive: (m) => !!m({ to: '/secrets' }),
    requires: Actions.SecretRead,
  },
  {
    // The section is Operations: playbooks, runs and the OS image inventory.
    // The route keeps the historical /workflows path — only the label moved.
    label: t('widgets.header.nav.operations'),
    to: '/workflows',
    search: { sidebar: undefined, tab: 'templates', templateId: undefined },
    isActive: (m) => !!m({ to: '/workflows' }),
    requires: Actions.PlaybookRead,
  },
  {
    label: t('widgets.header.nav.utilities'),
    to: '/utilities',
    search: { sidebar: undefined },
    isActive: (m) => !!m({ to: '/utilities' }),
    requires: Actions.UtilityRead,
  },
  {
    // What needs an operator now, and what the platform has been doing. Gated
    // on node.read like the sections it summarises: both tabs are views over
    // nodes, and neither adds a permission of its own.
    label: t('widgets.header.nav.status'),
    to: '/status',
    search: { sidebar: undefined, tab: 'attention' },
    isActive: (m) => !!m({ to: '/status' }),
    requires: Actions.NodeRead,
    showsAttentionCount: true,
  },
]

export const Header = () => {
  const user = authHooks.useCurrentUser()
  const navigate = useNavigate()
  const matchRoute = useMatchRoute()
  const [mobileOpen, setMobileOpen] = useState(false)
  // Hide the sections this member cannot read. Secrets is the one that mattered
  // in practice: every role below admin was offered it, and the 403 behind it
  // rendered as "create your first secret" — an invitation to an action whose
  // own button is hidden.
  const canAnyScope = useCanAnyScope()
  const allNavItems = navItems()
  const visibleNavItems = allNavItems.filter((item) =>
    canAnyScope(item.requires),
  )
  // Only asked for when the section is visible to this member: the count is a
  // fleet resolve, and spending a 403 on it every page load buys nothing.
  const attentionCount = useAttentionCount(canAnyScope(Actions.NodeRead))

  if (!user) {
    return null
  }

  const go = (item: NavItem) => {
    setMobileOpen(false)
    navigate({ to: item.to, search: item.search as never, replace: true })
  }

  const linkClass = (active: boolean, extra = '') =>
    clsx(
      'font-mono text-sm transition-colors',
      active ? 'text-lime-6' : 'text-neutral-4 hover:text-neutral-2',
      extra,
    )

  return (
    <div className="flex-none flex h-16 items-center justify-between bg-neutral-14 px-3 md:px-8">
      <div className="flex items-center h-full min-w-0">
        <button
          type="button"
          onClick={() => go(allNavItems[0])}
          className="flex flex-none items-center h-full"
          aria-label={t('widgets.header.home')}
        >
          <AssetLogo className="h-8 flex-none" />
        </button>

        {/* Desktop nav */}
        <nav className="hidden md:flex h-full ml-8 lg:ml-[100px]">
          {visibleNavItems.map((item) => {
            const active = item.isActive(matchRoute)
            return (
              <button
                key={item.to}
                type="button"
                onClick={() => go(item)}
                className={linkClass(
                  active,
                  'h-full flex items-center justify-center px-1 ml-6 first:ml-0 border-b-2 ' +
                    (active ? 'border-lime-6' : 'border-transparent'),
                )}
              >
                {/* The count hangs off the label as a superscript rather
                    than sitting in the row as a filled pill: it is an
                    at-a-glance number, not an alarm, and a solid yellow chip
                    beside the label out-shouted every other section. */}
                <span className="relative">
                  {item.label}
                  {item.showsAttentionCount && attentionCount > 0 && (
                    <span
                      className="absolute -top-1.5 -right-2.5 text-xxs leading-none text-yellow-6"
                      aria-label={t('widgets.header.nav.attentionCount', {
                        count: attentionCount,
                      })}
                    >
                      {attentionCount}
                    </span>
                  )}
                </span>
              </button>
            )
          })}
        </nav>
      </div>

      <div className="flex items-center gap-2">
        {/* Mobile hamburger nav */}
        <div className="relative md:hidden">
          <button
            type="button"
            className="p-2 text-neutral-4 hover:text-neutral-2"
            aria-label={t('widgets.header.menu')}
            aria-expanded={mobileOpen}
            onClick={() => setMobileOpen((v) => !v)}
          >
            <svg
              className="w-6 h-6"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M4 6h16M4 12h16M4 18h16"
              />
            </svg>
          </button>
          {mobileOpen && (
            <>
              <div
                className="fixed inset-0 z-40"
                onClick={() => setMobileOpen(false)}
              />
              <div className="absolute right-0 mt-2 bg-neutral-12 rounded py-2 flex flex-col min-w-48 z-50 shadow-lg">
                {visibleNavItems.map((item) => {
                  const active = item.isActive(matchRoute)
                  return (
                    <button
                      key={item.to}
                      type="button"
                      onClick={() => go(item)}
                      className={linkClass(active, 'text-left px-4 py-2')}
                    >
                      <span className="relative">
                        {item.label}
                        {item.showsAttentionCount && attentionCount > 0 && (
                          <span
                            className="absolute -top-1.5 -right-2.5 text-xxs leading-none text-yellow-6"
                            aria-label={t('widgets.header.nav.attentionCount', {
                              count: attentionCount,
                            })}
                          >
                            {attentionCount}
                          </span>
                        )}
                      </span>
                    </button>
                  )
                })}
              </div>
            </>
          )}
        </div>

        {getConfigValue('VITE_ESSENTIALS_API_ONLY') !== 'on' && (
          <Menu>
            <MenuButton>
              <UserAvatar user={user} size="xxs" />
            </MenuButton>
            {/* An anchored MenuItems is rendered in a portal on document.body,
                outside the header's stacking context — so the header's own
                z-index does nothing for it and, unpositioned, it lost to both
                the header and the node panel (z-30) it drops into. It needs a
                z-index of its own, above every layer it can overlap.

                modal={false}: the default modal mode scroll-locks the document
                and pads body by a scrollbar width the layout never gave up
                (html carries scrollbar-gutter: stable), so opening the menu
                shifted the whole page left. A small dropdown
                needs no scroll lock — outside-click already closes it. */}
            <MenuItems anchor="bottom end" modal={false} className="mt-3 z-50">
              <AccountMenu />
            </MenuItems>
          </Menu>
        )}
      </div>
    </div>
  )
}
