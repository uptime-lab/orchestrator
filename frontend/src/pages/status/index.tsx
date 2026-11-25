import { useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { OU_FILTER_ALL, OU_FILTER_UNASSIGNED } from '@/widgets/ouRail'
import { OURailAside } from '@/widgets/ouRail/OURailAside.tsx'

import { useNodes } from '@/entities/node/hooks.ts'

import { AttentionTab } from './attentionTab'
import { LogsTab } from './logsTab'
import { routeApi } from './shared'

export type StatusTab = 'attention' | 'logs'

const TABS: StatusTab[] = ['attention', 'logs']

// The Status section: what needs an operator now, and what the platform has
// been doing. Both tabs are views over nodes, so both share the OU rail and
// the same `?ou=` filter the Nodes page uses.
export const StatusPage = () => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = routeApi.useSearch()
  const { nodes } = useNodes()

  const tab = search.tab
  const selectedOU = search.ou ?? OU_FILTER_ALL

  // Both endpoints take a unit id or the literal "unassigned"; the rail's
  // "all" sentinel means no narrowing and is not sent.
  const ouParam = selectedOU === OU_FILTER_ALL ? undefined : selectedOU

  // The rail counts nodes, because a node is what both tabs filter. Counting
  // attention rows here would make the Logs tab's rail say something it does
  // not filter by, and counting log rows is not a question one page of a
  // server-paged list can answer.
  const ouCounts = useMemo(() => {
    const counts: Record<string, number> = {}
    for (const node of nodes) {
      if (node.ou_id) {
        counts[node.ou_id] = (counts[node.ou_id] ?? 0) + 1
      }
    }
    return counts
  }, [nodes])

  const unassignedCount = useMemo(
    () => nodes.filter((node) => !node.ou_id).length,
    [nodes],
  )

  const setOU = (value: string) =>
    void navigate({
      to: '.',
      search: (prev) => ({
        ...prev,
        ou: value === OU_FILTER_ALL ? undefined : value,
        // A unit change is a new question; page 4 of the old one is not an
        // answer to it.
        page: undefined,
      }),
      replace: true,
    })

  const setTab = (next: StatusTab) =>
    void navigate({
      to: '.',
      search: (prev) => ({
        ...prev,
        tab: next,
        // The two tabs have disjoint filters. Carrying one tab's filter into
        // the other silently narrows a list the operator did not narrow.
        conditions: undefined,
        status: undefined,
        slug: undefined,
        page: undefined,
        sidebar: undefined,
        nodeId: undefined,
      }),
      replace: true,
    })

  return (
    <div className="h-full w-full flex">
      <OURailAside
        storageKey="status-ou-panel-collapsed"
        counts={ouCounts}
        selected={selectedOU}
        onSelect={setOU}
        pseudoRows={[
          {
            value: OU_FILTER_ALL,
            label: t('pages.status.rail.all'),
            count: nodes.length,
          },
          {
            value: OU_FILTER_UNASSIGNED,
            label: t('pages.status.rail.unassigned'),
            count: unassignedCount,
          },
        ]}
      />

      <div className="flex-1 min-w-0 overflow-y-auto p-4 pt-8 md:p-8">
        <h1 className="text-lg uppercase font-bold mb-6">
          {t('pages.status.title')}
        </h1>

        <div className="flex gap-1 border-b border-neutral-12 mb-6">
          {TABS.map((item) => (
            <button
              key={item}
              type="button"
              onClick={() => setTab(item)}
              className={clsx(
                'font-mono text-sm px-3 py-2 border-b-2 -mb-px transition-colors',
                tab === item
                  ? 'text-lime-6 border-lime-6'
                  : 'text-neutral-4 hover:text-neutral-2 border-transparent',
              )}
            >
              {t(`pages.status.tabs.${item}` as const)}
            </button>
          ))}
        </div>

        {tab === 'attention' ? (
          <AttentionTab ouParam={ouParam} />
        ) : (
          <LogsTab ouParam={ouParam} />
        )}
      </div>
    </div>
  )
}
