import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'
import { TanStackRouterDevtools } from '@tanstack/router-devtools'

import { ReauthDialogHost } from '@/widgets/reauthDialog'

import { AuthState } from '@/entities/auth/types'

export const Route = createRootRouteWithContext<{
  auth: AuthState
}>()({
  // The re-auth prompt is mounted at the root because the request that needs it
  // can originate anywhere: the interceptor suspends that request until this
  // answers, so it must outlive whichever screen started it.
  component: () => (
    <>
      <Outlet />
      <ReauthDialogHost />
      {import.meta.env.DEV && <TanStackRouterDevtools />}
    </>
  ),
})
