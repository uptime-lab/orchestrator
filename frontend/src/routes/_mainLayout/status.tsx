import { createFileRoute } from '@tanstack/react-router'

import { StatusPage, type StatusTab } from '@/pages/status'

import { SidebarEnum } from '@/widgets/sidebars/types'

import { nodesModel } from '@/entities/node'
import { ousModel } from '@/entities/ou'

const isStatusTab = (v: unknown): v is StatusTab =>
  v === 'attention' || v === 'logs'

const str = (v: unknown): string | undefined =>
  typeof v === 'string' && v ? v : undefined

const page = (v: unknown): number | undefined => {
  const n = typeof v === 'string' ? Number(v) : typeof v === 'number' ? v : NaN
  return Number.isInteger(n) && n > 1 ? n : undefined
}

export const Route = createFileRoute('/_mainLayout/status')({
  validateSearch: (search: Record<string, unknown>) => {
    const base = {
      // Attention is the default: it is the view that says whether anything
      // needs doing, and the log is what you open once you know it does.
      tab: (isStatusTab(search.tab) ? search.tab : 'attention') as StatusTab,
      ou: str(search.ou),
      // Comma-separated filter sets, kept as raw strings here and parsed by
      // the tab that owns them — the server refuses an unknown value, so a
      // hand-edited URL fails loudly rather than silently matching everything.
      // Named `conditions`, not `kind`: the secrets route already declares a
      // `kind` search param with a narrower type, and the layout shares one
      // search union across its children.
      conditions: str(search.conditions),
      status: str(search.status),
      slug: str(search.slug),
      since: str(search.since),
      node: str(search.node),
      page: page(search.page),
    }
    if (
      search.sidebar === SidebarEnum.Node &&
      typeof search.nodeId === 'string'
    ) {
      return { ...base, sidebar: SidebarEnum.Node, nodeId: search.nodeId }
    }
    return { ...base, sidebar: undefined, nodeId: undefined }
  },
  component: () => <StatusPage />,
  // allSettled for the same reason as the credentials loader: the OU tree and
  // the node inventory are separate grants, and one refusal must not replace
  // the page. The two fleet reads load through TanStack Query in the page.
  loader: async () => {
    await Promise.allSettled([ousModel.fetchOUsFx(), nodesModel.fetchNodesFx()])
  },
})
