import { createFileRoute } from '@tanstack/react-router'
import { createRootLocator } from 'create-locator'

import { HomePage, HomePageLocator } from '@/pages/home'

export const homeRouteLocator = createRootLocator<HomePageLocator>('homePage', {
  isProduction: false, // the library removes test-ids in production environment, but we want to keep them for analytics purposes and so on
  parameterAttributePrefix: 'data-test-',
  pathAttribute: 'data-testid',
  pathSeparator: '-',
})

type IndexPageSearch = {
  ou?: string
}

export const Route = createFileRoute('/_mainLayout/')({
  component: () => <HomePage {...homeRouteLocator()} />,
  validateSearch: (search: Record<string, unknown>): IndexPageSearch => ({
    ou: typeof search.ou === 'string' ? search.ou : undefined,
  }),
})
