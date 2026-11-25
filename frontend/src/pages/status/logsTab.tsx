import { useNavigate } from '@tanstack/react-router'
import clsx from 'clsx'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { SidebarEnum } from '@/widgets/sidebars/types'

import {
  ACTIVITY_STATUSES,
  useFleetActivity,
  type ActivityRow,
  type ActivityStatus,
} from '@/entities/fleet'
import { useOUs } from '@/entities/ou'

import Badge, { type BadgeColor } from '@/shared/ui/badge'
import { Button } from '@/shared/ui/button/button.tsx'
import { Column, DataTable } from '@/shared/ui/dataTable'
import { Input } from '@/shared/ui/input'

import {
  parseCsv,
  routeApi,
  SINCE_WINDOWS,
  sinceTimestamp,
  toCsv,
  type SinceWindow,
} from './shared'

const PER_PAGE = 50

// Three terminal states are deliberately not failures, and colouring them red
// would have an operator chasing commands the platform retired on purpose:
// superseded (a newer command for the same target replaced it unsent), expired
// (it waited past its deadline for a node that never came back) and skipped (a
// precondition refused it on arrival, usually an agent below the slug's floor).
const STATUS_COLOR: Record<ActivityStatus, BadgeColor> = {
  registered: 'neutral',
  sent: 'blue',
  in_progress: 'blue',
  success: 'lime',
  failed: 'red',
  superseded: 'neutral',
  expired: 'amber',
  skipped: 'amber',
}

type Props = {
  ouParam?: string
}

export const LogsTab = ({ ouParam }: Props) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = routeApi.useSearch()
  const { ous } = useOUs()

  const selectedStatuses = useMemo(
    () => parseCsv(search.status),
    [search.status],
  )
  const window = (search.since ?? '7d') as SinceWindow
  const page = search.page ?? 1

  const { data, isLoading, isError, isFetching } = useFleetActivity({
    ouId: ouParam,
    nodeId: search.node,
    statuses: selectedStatuses,
    slug: search.slug,
    since: sinceTimestamp(window),
    page,
    perPage: PER_PAGE,
  })

  const unitName = useMemo(() => {
    const byId = new Map(ous.map((ou) => [ou.id, ou.name]))
    return (id?: string) => (id ? byId.get(id) : undefined)
  }, [ous])

  // Any filter change resets to the first page: page 4 of the old filter is
  // rarely page 4 of the new one, and an empty table reads as "no results".
  const setFilter = (patch: Record<string, unknown>) =>
    void navigate({
      to: '.',
      search: (prev) => ({ ...prev, ...patch, page: undefined }),
      replace: true,
    })

  const toggleStatus = (status: string) => {
    const next = selectedStatuses.includes(status)
      ? selectedStatuses.filter((s) => s !== status)
      : [...selectedStatuses, status]
    setFilter({ status: toCsv(next) })
  }

  const total = data?.total ?? 0
  const lastPage = Math.max(1, Math.ceil(total / PER_PAGE))

  const columns: Column<ActivityRow>[] = [
    {
      key: 'time',
      header: t('pages.status.logs.table.time'),
      alwaysVisible: true,
      render: (row) => (
        <span className="font-mono text-xs text-neutral-4">
          {new Date(row.created_at).toLocaleString()}
        </span>
      ),
      sortValue: (row) => row.created_at,
    },
    {
      key: 'node',
      header: t('pages.status.logs.table.node'),
      alwaysVisible: true,
      render: (row) =>
        row.node_name ?? (
          <span className="font-mono text-xs text-neutral-6">
            {row.node_id}
          </span>
        ),
      sortValue: (row) => (row.node_name ?? row.node_id).toLowerCase(),
    },
    {
      key: 'unit',
      header: t('pages.status.logs.table.unit'),
      defaultHidden: true,
      render: (row) => unitName(row.ou_id) ?? '',
      sortValue: (row) => (unitName(row.ou_id) ?? '').toLowerCase(),
    },
    {
      key: 'command',
      header: t('pages.status.logs.table.command'),
      alwaysVisible: true,
      render: (row) => (
        <span className="font-mono text-xs text-neutral-2">{row.slug}</span>
      ),
      sortValue: (row) => row.slug,
    },
    {
      key: 'status',
      header: t('pages.status.logs.table.status'),
      alwaysVisible: true,
      render: (row) => (
        <div className="flex items-center gap-2">
          <Badge
            variant="outline"
            color={STATUS_COLOR[row.status] ?? 'neutral'}
          >
            {t(`pages.status.logs.statuses.${row.status}` as never, {
              defaultValue: row.status,
            })}
          </Badge>
          {/* A queued command was accepted, not applied. */}
          {row.queueable && row.status === 'registered' && (
            <span className="text-xs text-neutral-6">
              {t('pages.status.logs.queued')}
            </span>
          )}
        </div>
      ),
      sortValue: (row) => row.status,
    },
    {
      key: 'details',
      header: t('pages.status.logs.table.details'),
      render: (row) => (
        <span className="text-xs text-neutral-5 line-clamp-2">
          {row.details ?? ''}
        </span>
      ),
    },
  ]

  if (isError) {
    return <p className="text-sm text-red-5">{t('pages.status.logs.failed')}</p>
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        {ACTIVITY_STATUSES.map((status) => {
          const active = selectedStatuses.includes(status)
          return (
            <button
              key={status}
              type="button"
              onClick={() => toggleStatus(status)}
              className={clsx(
                'rounded-[3px] border px-2 py-[3px] text-xs transition-colors',
                active
                  ? 'border-lime-6 text-lime-6'
                  : 'border-neutral-11 text-neutral-4 hover:text-neutral-2',
              )}
            >
              {t(`pages.status.logs.statuses.${status}` as never, {
                defaultValue: status,
              })}
            </button>
          )
        })}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <div className="flex gap-1">
          {SINCE_WINDOWS.map((option) => (
            <button
              key={option}
              type="button"
              onClick={() => setFilter({ since: option })}
              className={clsx(
                'rounded-[3px] border px-2 py-[3px] text-xs transition-colors',
                window === option
                  ? 'border-lime-6 text-lime-6'
                  : 'border-neutral-11 text-neutral-4 hover:text-neutral-2',
              )}
            >
              {t(`pages.status.logs.windows.${option}` as never)}
            </button>
          ))}
        </div>
        <Input
          value={search.slug ?? ''}
          onChange={(e) => setFilter({ slug: e.target.value || undefined })}
          placeholder={t('pages.status.logs.slugPlaceholder')}
          className="w-56"
        />
        {search.node && (
          <button
            type="button"
            onClick={() => setFilter({ node: undefined })}
            className="text-xs text-neutral-4 hover:text-neutral-2 underline"
          >
            {t('pages.status.logs.clearNodeFilter')}
          </button>
        )}
      </div>

      <DataTable
        storageKey="status-logs-table"
        columnsLabel={t('pages.status.columns')}
        columns={columns}
        rows={data?.activity ?? []}
        rowKey={(row) => row.id}
        isSelected={(row) =>
          search.sidebar === SidebarEnum.Node && search.nodeId === row.node_id
        }
        onRowClick={(row) =>
          void navigate({
            to: '.',
            search: (prev) => ({
              ...prev,
              sidebar: SidebarEnum.Node,
              nodeId: row.node_id,
            }),
          })
        }
        empty={
          <p className="text-sm text-neutral-6">
            {isLoading
              ? t('pages.status.loading')
              : t('pages.status.logs.empty')}
          </p>
        }
      />

      <div className="flex items-center justify-between gap-4">
        <span className="text-xs text-neutral-6">
          {t('pages.status.logs.total', { count: total })}
          {isFetching && ` · ${t('pages.status.loading')}`}
        </span>
        {lastPage > 1 && (
          <div className="flex items-center gap-2">
            <Button
              kind="secondary"
              size="sm"
              disabled={page <= 1}
              onClick={() =>
                void navigate({
                  to: '.',
                  search: (prev) => ({ ...prev, page: page - 1 }),
                  replace: true,
                })
              }
            >
              {t('pages.status.logs.previous')}
            </Button>
            <span className="font-mono text-xs text-neutral-5">
              {t('pages.status.logs.page', { page, lastPage })}
            </span>
            <Button
              kind="secondary"
              size="sm"
              disabled={page >= lastPage}
              onClick={() =>
                void navigate({
                  to: '.',
                  search: (prev) => ({ ...prev, page: page + 1 }),
                  replace: true,
                })
              }
            >
              {t('pages.status.logs.next')}
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
