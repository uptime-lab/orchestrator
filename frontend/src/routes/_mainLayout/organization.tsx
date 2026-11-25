import { createFileRoute } from '@tanstack/react-router'

import { isUnitTab, OrganizationPage } from '@/pages/organization'

import { nodesModel } from '@/entities/node'
import { ousModel } from '@/entities/ou'

// The Organization section: unit tree on the left, the selected unit's
// passport on the right in two tabs — Policies (with inheritance) and Contents
// (everything the unit delivers). No `ou` selected = the account defaults page.
// `tab` is only meaningful with a unit selected; an unknown value falls back to
// Policies so a stale link never lands on an empty page.
export const Route = createFileRoute('/_mainLayout/organization')({
  validateSearch: (search: Record<string, unknown>) => ({
    ou: typeof search.ou === 'string' ? search.ou : undefined,
    create: search.create === true || search.create === 'true' || undefined,
    tab:
      isUnitTab(search.tab) && search.tab !== 'policies'
        ? search.tab
        : undefined,
  }),
  component: () => {
    const search = Route.useSearch()
    return (
      <OrganizationPage
        selectedOU={search.ou}
        createOpen={search.create}
        tab={search.tab ?? 'policies'}
      />
    )
  },
  // allSettled: the tree and the node counts are separate grants; a role that
  // may read one and not the other must still get a usable page.
  loader: async () => {
    await Promise.allSettled([ousModel.fetchOUsFx(), nodesModel.fetchNodesFx()])
  },
})
