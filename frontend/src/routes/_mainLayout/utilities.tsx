import { createFileRoute } from '@tanstack/react-router'

import { UtilitiesPage } from '@/pages/utilities'

import { nodesModel } from '@/entities/node'
import { ousModel } from '@/entities/ou'

// Tab state lives in the URL, like the credentials/secrets pages. Assignments
// is the default: it is the working view; the catalog is the reading room.
export type UtilitiesTab = 'catalog' | 'assignments'

const isUtilitiesTab = (v: unknown): v is UtilitiesTab =>
  v === 'catalog' || v === 'assignments'

export const Route = createFileRoute('/_mainLayout/utilities')({
  validateSearch: (search: Record<string, unknown>) => {
    const tab: UtilitiesTab = isUtilitiesTab(search.tab)
      ? search.tab
      : 'assignments'
    const ou =
      typeof search.ou === 'string' && search.ou ? search.ou : undefined
    const sidebar = search.sidebar

    if (
      sidebar === 'utilityAssignmentDetails' &&
      typeof search.assignmentId === 'string'
    ) {
      return {
        tab,
        ou,
        sidebar: 'utilityAssignmentDetails' as const,
        assignmentId: search.assignmentId,
      }
    }
    // OU sidebars reachable from the rail's edit mode.
    return { tab, ou, sidebar: undefined }
  },
  component: () => <UtilitiesPage />,
  // allSettled for the same reason as the credentials loader: the OU tree and
  // the node inventory are separate grants, and one refusal must not replace
  // the page. Assignments/catalog load through TanStack Query in the page.
  loader: async () => {
    await Promise.allSettled([ousModel.fetchOUsFx(), nodesModel.fetchNodesFx()])
  },
})
