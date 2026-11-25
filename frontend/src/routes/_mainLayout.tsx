import {
  createFileRoute,
  Outlet,
  redirect,
  useNavigate,
} from '@tanstack/react-router'
import clsx from 'clsx'
import { useCallback } from 'react'

import { Header } from '@/widgets/header'
import { SideBarRenderer } from '@/widgets/sidebars'
import {
  isSidebar,
  SidebarEnum,
  SidebarWidgetParams,
} from '@/widgets/sidebars/types.ts'

import { authHooks } from '@/entities/auth'
import { AuthStatus } from '@/entities/auth/types'
import { SessionSwitchedNotice } from '@/entities/auth/ui/SessionSwitchedNotice.tsx'
import { nodesModel } from '@/entities/node'
import { ousModel } from '@/entities/ou'
import { ConnectionLostBadge } from '@/entities/userCommonSocket/ui/ConnectionLostBadge.tsx'

import { ToastHost } from '@/shared/ui/toast'

export const parseSidebarSearch = (
  search: Record<string, unknown>,
): SidebarWidgetParams => {
  if (!isSidebar(search.sidebar)) {
    return { sidebar: undefined }
  }
  if (
    search.sidebar === SidebarEnum.Node ||
    search.sidebar === SidebarEnum.DeletedNode
  ) {
    return typeof search.nodeId === 'string'
      ? { sidebar: search.sidebar, nodeId: search.nodeId }
      : { sidebar: undefined }
  }
  if (search.sidebar === SidebarEnum.OSImageDetails) {
    return typeof search.imageId === 'string'
      ? { sidebar: search.sidebar, imageId: search.imageId }
      : { sidebar: undefined }
  }
  if (search.sidebar === SidebarEnum.SSHKeyDetails) {
    return typeof search.keyId === 'string'
      ? { sidebar: search.sidebar, keyId: search.keyId }
      : { sidebar: undefined }
  }
  if (search.sidebar === SidebarEnum.EditSSHKey) {
    return typeof search.keyId === 'string'
      ? { sidebar: search.sidebar, keyId: search.keyId }
      : { sidebar: undefined }
  }
  if (search.sidebar === SidebarEnum.WorkflowExecutionDetails) {
    return typeof search.executionId === 'string'
      ? { sidebar: search.sidebar, executionId: search.executionId }
      : { sidebar: undefined }
  }
  if (search.sidebar === SidebarEnum.MasterTokenDetails) {
    return typeof search.tokenId === 'string'
      ? { sidebar: search.sidebar, tokenId: search.tokenId }
      : { sidebar: undefined }
  }
  if (search.sidebar === SidebarEnum.SecretDetails) {
    return typeof search.secretId === 'string'
      ? { sidebar: search.sidebar, secretId: search.secretId }
      : { sidebar: undefined }
  }
  if (search.sidebar === SidebarEnum.AccessUserDetails) {
    return typeof search.entryId === 'string'
      ? { sidebar: search.sidebar, entryId: search.entryId }
      : { sidebar: undefined }
  }
  if (search.sidebar === SidebarEnum.UtilityAssignmentDetails) {
    return typeof search.assignmentId === 'string'
      ? { sidebar: search.sidebar, assignmentId: search.assignmentId }
      : { sidebar: undefined }
  }
  return { sidebar: search.sidebar }
}

type MainLayoutSearch = {
  tab?: string
  view?: string // For sidebar view state (overview, history, etc.)
  ou?: string // OU tree filter on the nodes page: 'all' | 'unassigned' | <uuid>
}

export const Route = createFileRoute('/_mainLayout')({
  validateSearch: (search: Record<string, unknown>) =>
    ({
      ...parseSidebarSearch(search),
      tab: typeof search.tab === 'string' ? search.tab : undefined,
      view: typeof search.view === 'string' ? search.view : undefined,
      ou: typeof search.ou === 'string' ? search.ou : undefined,
    }) as SidebarWidgetParams & MainLayoutSearch,
  beforeLoad: async ({ context, location }) => {
    if (context.auth.status !== AuthStatus.LoggedIn) {
      throw redirect({
        to: '/login',
        search: {
          redirect: location.href,
        },
      })
    }
  },
  component: () => {
    const search = Route.useSearch()
    const navigate = useNavigate()

    // Reactively leave for /login when the session ends (sign out, membership
    // revoked). The beforeLoad guard only runs on navigation, so a sign-out
    // while sitting on a page needs this to redirect without a manual refresh.
    authHooks.useAuthOnlyRoute()

    const handleSideBarClose = useCallback(() => {
      const {
        sidebar: _,
        executionId: __,
        ...searchWithoutSidebar
      } = search as Record<string, unknown>
      navigate({ to: '.', search: searchWithoutSidebar, replace: true })
    }, [search, navigate])

    return (
      <>
        <ConnectionLostBadge />
        <SessionSwitchedNotice />
        {/* Mounted once for the whole authenticated app: before this there was no
            way to tell the user anything, so failures went to console.error. */}
        <ToastHost />
        {/* relative + z-40 keeps the header and the dropdowns it renders inline
            (the mobile nav) above the node panel (z-30). Menus that HeadlessUI
            anchors escape into a body portal and are unaffected by this — they
            carry their own z-index instead. */}
        <div className="relative z-40 flex-none">
          <Header />
        </div>
        <div className="relative flex flex-1 min-h-0">
          <div
            className={clsx(
              // min-h-0 lets this flex child be constrained by the row instead
              // of growing past the viewport; overflow-y-hidden keeps vertical
              // scroll on the page's own content pane (and the OU rail), so the
              // header and rail stay fixed while only the middle scrolls.
              // overflow-x-auto stays for wide content (tables, canvases).
              'flex-1 h-full min-h-0 overflow-x-auto overflow-y-hidden',
              !!search.sidebar && 'hidden md:block',
            )}
          >
            <Outlet />
          </div>
          {!!search.sidebar && (
            <div
              className={clsx(
                // The panel overlays the content instead of squeezing it: the
                // list underneath keeps its width (and stays clickable left of
                // the panel). On desktop it floats as a distinct card (inset,
                // rounded, bordered) so it clearly reads as a layer above the
                // page rather than a part of it. Scroll internally with a
                // reserved scrollbar gutter so switching tabs (short vs tall
                // content) doesn't shift the layout.
                'absolute inset-y-0 right-0 z-30 max-w-full w-full bg-neutral-14 overflow-y-auto [scrollbar-gutter:stable]',
                'md:top-3 md:bottom-3 md:right-3 md:rounded-xl md:border md:border-neutral-11',
                'md:shadow-[-32px_0_64px_-8px_rgba(0,0,0,0.8)]',
                search.sidebar === SidebarEnum.WorkflowExecutionDetails
                  ? 'md:w-2/3'
                  : 'md:w-sidebar',
              )}
            >
              <SideBarRenderer params={search} onClose={handleSideBarClose} />
            </div>
          )}
        </div>
      </>
    )
  },
  loader: async () => {
    await Promise.allSettled([
      // more fetches coming
      nodesModel.fetchNodesFx(),
      ousModel.fetchOUsFx(),
      nodesModel.fetchFavoritesFx(),
    ])
  },
  loaderDeps: () => ({ shouldCache: true }),
  staleTime: 1000 * 60 * 15, // 15 min
})
