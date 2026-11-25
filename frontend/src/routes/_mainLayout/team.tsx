import { createFileRoute } from '@tanstack/react-router'
import { createRootLocator } from 'create-locator'

import { TeamPage, TeamPageLocator } from '@/pages/team'

import { usersModel } from '@/entities/user'

export const teamRouteLocator = createRootLocator<TeamPageLocator>('teamPage', {
  isProduction: false, // the library removes test-ids in production environment, but we want to keep them for analytics purposes and so on
  parameterAttributePrefix: 'data-test-',
  pathAttribute: 'data-testid',
  pathSeparator: '-',
})

export const Route = createFileRoute('/_mainLayout/team')({
  component: () => <TeamPage {...teamRouteLocator()} />,
  loader: async () => {
    await usersModel.fetchUsersFx()
  },
})
