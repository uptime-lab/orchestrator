import { createRouter, RouterProvider } from '@tanstack/react-router'
import { useUnit } from 'effector-react'
import { t } from 'i18next'
import { StrictMode, useMemo } from 'react'
import ReactDOM from 'react-dom/client'

import './index.css'
import './i18n.ts'

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { authHooks, authModel } from './entities/auth/index.ts'
import { AuthStatus } from './entities/auth/types.ts'
import { RouteError } from './shared/ui/routeError'
// Side-effect import: registers the permission-map load-on-signin wiring.
import './entities/permissions/model.ts'

// Import the generated route tree
import { routeTree } from './routeTree.gen.ts'

// Initialising router and its' types
const router = createRouter({
  routeTree,
  context: {
    auth: authModel.DEFAULT_AUTH_STATE,
  },
  // The app's only error boundary. Without it there was none anywhere — no
  // errorComponent, no defaultErrorComponent, no componentDidCatch — so one
  // unexpected shape from an older agent in the node sidebar data rendered a
  // blank white page with no route back.
  defaultErrorComponent: ({ error }) => <RouteError error={error} />,
})
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

// Create a client
const queryClient = new QueryClient()

const App = () => {
  const auth = authHooks.useAuthInit()
  const isInitializing = useUnit(authModel.initAuthFx.pending)
  const isUnreachable = useUnit(authModel.$bootUnreachable)

  const routerContext = useMemo(() => ({ auth: auth.state }), [auth.state])

  // Wait for initialization to complete (either success or failure)
  // Don't render router until we know if user is authenticated or not
  if (auth.state.status === AuthStatus.Idle) {
    // The stored session is kept while the server cannot be reached; the model
    // retries on its own and the button only skips the wait.
    if (isUnreachable) {
      return (
        <RouteError
          title={t('shared.serverUnreachable.title')}
          body={t('shared.serverUnreachable.body')}
          retry={{
            label: isInitializing
              ? t('shared.serverUnreachable.retrying')
              : t('shared.serverUnreachable.retry'),
            onClick: () => authModel.retryAuthInit(),
            disabled: isInitializing,
          }}
        />
      )
    }
    if (isInitializing) {
      return null // Still initializing, show loading
    }
    // If not initializing and still idle, check if we should have initialized
    // This handles the case where tokens don't exist (will be handled by router redirect)
  }

  return <RouterProvider router={router} context={routerContext} />
}

// Render the app
const rootElement = document.getElementById('root')!
if (!rootElement.innerHTML) {
  const root = ReactDOM.createRoot(rootElement)
  root.render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <App />
      </QueryClientProvider>
    </StrictMode>,
  )
}
