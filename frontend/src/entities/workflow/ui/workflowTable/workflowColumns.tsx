import { t } from 'i18next'

import type { WorkflowTemplate } from '../../api/types'
import { isWorkflowActive, scopeOUIDs } from '../../lib/scope'

// The next run the scheduler will actually start. The scheduler runs active
// playbooks only, so a draft has none, whatever its schedule computes to.
export const nextRunOf = (template: WorkflowTemplate): string | undefined =>
  isWorkflowActive(template) ? template.next_run_at || undefined : undefined

// Column model for the workflow table, mirroring the image/node list
// approach: header and rows render from the same ordered definition.
export type WorkflowColumn = {
  key: 'blocks' | 'status' | 'schedule' | 'ou' | 'description' | 'created'
  // A FUNCTION, not a string: the label comes from i18next, which is not
  // initialised when this module is first evaluated (see nodeColumns.tsx for
  // the full reasoning). The array itself stays a const — unlike nodeColumns —
  // because every string in it is already lazy, so there is nothing a
  // function wrapper would defer; workflowColumns.test.ts reads it directly.
  label: () => string
  className: string
  sortValue: (
    template: WorkflowTemplate,
    ouNames: Record<string, string>,
  ) => string | number
}

// Each optional column appears only once the table is wide enough to hold
// it next to everything already shown: the name (w-64), the row actions
// (w-32), the fixed columns and their gaps. Lower-priority columns appear
// later; the description, which flexes, needs at least 8rem of its own. A
// breakpoint below that sum made headers overlap ("DESCRCREATED") because
// the fixed columns do not shrink. workflowColumns.test.ts checks the sums.
export const WORKFLOW_COLUMNS: WorkflowColumn[] = [
  {
    key: 'blocks',
    label: () => t('entities.workflow.table.columns.blocks'),
    className: 'w-16 flex-shrink-0',
    sortValue: (template) => template.steps.length,
  },
  {
    key: 'status',
    label: () => t('entities.workflow.table.columns.status'),
    className: 'w-20 flex-shrink-0',
    sortValue: (template) => template.status ?? 'active',
  },
  {
    key: 'schedule',
    label: () => t('entities.workflow.table.columns.schedule'),
    className: 'w-44 flex-shrink-0 hidden @[70rem]:block',
    // Sorts by the next run, so the playbook firing soonest is one click away.
    // Manual playbooks sort last rather than first: an empty string would put
    // every one of them above the schedule the operator opened this column to
    // find.
    sortValue: (template) => {
      const next = nextRunOf(template)
      return next ? new Date(next).getTime() : Infinity
    },
  },
  {
    key: 'ou',
    label: () => t('entities.workflow.table.columns.ou'),
    className: 'w-36 flex-shrink-0 hidden @[58rem]:block',
    // Sorts by the first unit's name, so playbooks of the same unit group
    // together whatever else they are also attached to.
    sortValue: (template, ouNames) =>
      scopeOUIDs(template)
        .map((id) => ouNames[id] || '')
        .sort()
        .join(', '),
  },
  {
    key: 'description',
    label: () => t('entities.workflow.table.columns.description'),
    className: 'flex-1 min-w-[8rem] hidden @[80rem]:block',
    sortValue: (template) => template.description ?? '',
  },
  {
    key: 'created',
    label: () => t('entities.workflow.table.columns.created'),
    className: 'w-28 flex-shrink-0 hidden @[48rem]:block',
    sortValue: (template) => new Date(template.created_at).getTime(),
  },
]

// formatNextRun renders an upcoming run: the date, and the UTC time, because
// the schedule the operator wrote is in UTC and a local-time rendering of it
// would silently disagree with the expression beside it.
export const formatNextRun = (iso: string): string => {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  return t('entities.workflow.table.nextRunUTC', {
    date: date.toLocaleDateString(undefined, {
      day: '2-digit',
      month: 'short',
      timeZone: 'UTC',
    }),
    time: date.toLocaleTimeString(undefined, {
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
      timeZone: 'UTC',
    }),
  })
}

export const formatWorkflowDate = (iso: string): string => {
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return '—'
  return date.toLocaleDateString(undefined, {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
  })
}
