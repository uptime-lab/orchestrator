import clsx from 'clsx'
import { t } from 'i18next'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import type { WorkflowTemplate } from '../../api/types'
import { isWorkflowActive, scopeOUIDs } from '../../lib/scope'
import {
  formatNextRun,
  formatWorkflowDate,
  WORKFLOW_COLUMNS,
  type WorkflowColumn,
} from './workflowColumns'

type WorkflowSort = {
  key: string
  dir: 'asc' | 'desc'
}

// dataTransfer key for dragging a playbook row onto the OU filter tree.
export const WORKFLOW_DND_TYPE = 'text/workflow-id'

export type WorkflowTableProps = {
  templates: WorkflowTemplate[]
  // OU id -> display name, for the Scope column
  ouNames: Record<string, string>
  // Optional on purpose: the caller passes undefined for a viewer who lacks
  // playbook.author, and the row renders no action buttons at all rather than
  // disabled ones. A control that can never become enabled answers the
  // operator's question worse than its absence does. Opening the editor is
  // gated with the rest — it is the authoring surface, not a read view.
  onEdit?: (templateId: string) => void
  // Running is gated separately from authoring: playbook.run, not
  // playbook.author. A viewer who may run but not edit still gets this one.
  onRun?: (template: WorkflowTemplate) => void
  onClone?: (template: WorkflowTemplate) => void
  onExport?: (template: WorkflowTemplate) => void
  onDelete?: (template: WorkflowTemplate) => void
}

const RunIcon = () => (
  <svg className="w-4 h-4" viewBox="0 0 20 20" fill="currentColor">
    <path d="M6 4.5a1 1 0 011.53-.848l7 4.5a1 1 0 010 1.696l-7 4.5A1 1 0 016 13.5v-9z" />
  </svg>
)

const EditIcon = () => (
  <svg className="w-4 h-4" viewBox="0 0 20 20" fill="currentColor">
    <path d="M13.586 3.586a2 2 0 112.828 2.828l-8.5 8.5a1 1 0 01-.44.263l-3 1a1 1 0 01-1.265-1.265l1-3a1 1 0 01.263-.44l8.5-8.5z" />
  </svg>
)

const TrashIcon = () => (
  <svg className="w-4 h-4" viewBox="0 0 20 20" fill="currentColor">
    <path
      fillRule="evenodd"
      d="M9 2a1 1 0 00-.894.553L7.382 4H4a1 1 0 000 2v10a2 2 0 002 2h8a2 2 0 002-2V6a1 1 0 100-2h-3.382l-.724-1.447A1 1 0 0011 2H9zM7 8a1 1 0 012 0v6a1 1 0 11-2 0V8zm5-1a1 1 0 00-1 1v6a1 1 0 102 0V8a1 1 0 00-1-1z"
      clipRule="evenodd"
    />
  </svg>
)

const CloneIcon = () => (
  <svg className="w-4 h-4" viewBox="0 0 20 20" fill="currentColor">
    <path d="M7 3a2 2 0 012-2h6a2 2 0 012 2v8a2 2 0 01-2 2h-1v-2h1V3H9v1H7V3z" />
    <path d="M3 7a2 2 0 012-2h6a2 2 0 012 2v8a2 2 0 01-2 2H5a2 2 0 01-2-2V7zm2 0v8h6V7H5z" />
  </svg>
)

const ExportIcon = () => (
  <svg className="w-4 h-4" viewBox="0 0 20 20" fill="currentColor">
    <path d="M10 2a1 1 0 011 1v7.586l2.293-2.293a1 1 0 111.414 1.414l-4 4a1 1 0 01-1.414 0l-4-4a1 1 0 111.414-1.414L9 10.586V3a1 1 0 011-1z" />
    <path d="M4 14a1 1 0 011 1v1h10v-1a1 1 0 112 0v2a1 1 0 01-1 1H4a1 1 0 01-1-1v-2a1 1 0 011-1z" />
  </svg>
)

const WorkflowGlyph = () => (
  <svg
    className="w-5 h-5 text-neutral-4"
    viewBox="0 0 20 20"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.5"
  >
    <rect x="7" y="2" width="6" height="4" rx="1" />
    <rect x="2" y="14" width="6" height="4" rx="1" />
    <rect x="12" y="14" width="6" height="4" rx="1" />
    <path d="M10 6v3M10 9H5v5M10 9h5v5" />
  </svg>
)

const cellContent = (
  template: WorkflowTemplate,
  column: WorkflowColumn,
  ouNames: Record<string, string>,
) => {
  switch (column.key) {
    case 'blocks':
      return template.steps.length
    case 'status': {
      const isDraft = template.status === 'draft'
      return (
        <span
          className={
            isDraft
              ? 'border border-neutral-9 text-neutral-5 rounded-[3px] px-1.5 py-px text-xxs uppercase'
              : 'border border-lime-7 text-lime-5 rounded-[3px] px-1.5 py-px text-xxs uppercase'
          }
        >
          {isDraft
            ? t('entities.workflow.table.statusDraft')
            : t('entities.workflow.table.statusActive')}
        </span>
      )
    }
    case 'schedule': {
      if (!template.schedule_cron) {
        return (
          <span className="text-neutral-7">
            {t('entities.workflow.table.scheduleManual')}
          </span>
        )
      }
      // A draft's schedule never fires: the scheduler runs active playbooks
      // only.
      if (!isWorkflowActive(template)) {
        return (
          <span className="text-neutral-6" title={template.schedule_cron}>
            {t('entities.workflow.table.draftNotScheduled')}
          </span>
        )
      }
      // A schedule that can never fire is the case this column exists for: it
      // saves cleanly, the scheduler never picks it up, and nothing else in the
      // product ever says so.
      if (!template.next_run_at) {
        return (
          <span className="text-amber-9" title={template.schedule_cron}>
            {t('entities.workflow.table.neverFires')}
          </span>
        )
      }
      return (
        <span
          className="truncate block"
          title={`${template.schedule_description || template.schedule_cron} (${template.schedule_cron})`}
        >
          {formatNextRun(template.next_run_at)}
        </span>
      )
    }
    case 'ou': {
      // A playbook can live in several units; the column names them all rather
      // than picking one, because "which units does this apply to" is the
      // question the column exists to answer.
      const scope = scopeOUIDs(template)
      if (scope.length === 0) {
        return (
          <span className="text-neutral-6">
            {t('entities.workflow.table.allNodes')}
          </span>
        )
      }
      const names = scope.map(
        (id) => ouNames[id] || t('entities.workflow.table.unknownUnit'),
      )
      return (
        <span className="truncate block" title={names.join(', ')}>
          {names.length > 2
            ? `${names[0]} +${names.length - 1}`
            : names.join(', ')}
        </span>
      )
    }
    case 'description':
      return (
        <span className="truncate block" title={template.description}>
          {template.description || '—'}
        </span>
      )
    case 'created':
      return formatWorkflowDate(template.created_at)
    default:
      return null
  }
}

// Workflow templates as a sortable table (same interaction model as the
// image/node tables). Rows and the pencil open the canvas editor.
export const WorkflowTable = ({
  templates,
  ouNames,
  onEdit,
  onRun,
  onClone,
  onExport,
  onDelete,
}: WorkflowTableProps) => {
  const { t } = useTranslation()
  const [sort, setSort] = useState<WorkflowSort | null>(null)

  const toggleSort = (key: string) => {
    setSort((prev) => {
      if (prev?.key !== key) return { key, dir: 'asc' }
      if (prev.dir === 'asc') return { key, dir: 'desc' }
      return null
    })
  }

  const sortedTemplates = useMemo(() => {
    if (!sort) return templates
    const column =
      sort.key === 'name'
        ? {
            sortValue: (template: WorkflowTemplate) => template.name ?? '',
          }
        : WORKFLOW_COLUMNS.find((c) => c.key === sort.key)
    if (!column) return templates
    const factor = sort.dir === 'asc' ? 1 : -1
    return [...templates].sort((a, b) => {
      const va = column.sortValue(a, ouNames)
      const vb = column.sortValue(b, ouNames)
      if (typeof va === 'number' && typeof vb === 'number') {
        return (va - vb) * factor
      }
      return String(va).localeCompare(String(vb)) * factor
    })
  }, [templates, sort, ouNames])

  const sortHeader = (key: string, label: string) => (
    <button
      type="button"
      onClick={() => toggleSort(key)}
      className={clsx(
        'flex items-center gap-1 uppercase tracking-wide transition-colors',
        sort?.key === key
          ? 'text-lime-6'
          : 'text-neutral-7 hover:text-neutral-4',
      )}
    >
      {label}
      <span className="w-2">
        {sort?.key === key ? (sort.dir === 'asc' ? '↑' : '↓') : ''}
      </span>
    </button>
  )

  if (templates.length === 0) {
    return (
      <div className="text-center text-neutral-4 py-12">
        {t('entities.workflow.table.empty')}
      </div>
    )
  }

  return (
    <div className="@container flex flex-col gap-2">
      <div
        className={clsx(
          'hidden @[30rem]:flex items-center gap-4 px-4 pb-1',
          'text-xxs uppercase tracking-wide text-neutral-7 select-none',
        )}
      >
        <div className="w-64 flex-shrink-0 pl-11">
          {sortHeader('name', t('entities.workflow.table.columnPlaybook'))}
        </div>
        <div className="flex items-center gap-6 flex-1 min-w-0">
          {WORKFLOW_COLUMNS.map((column) => (
            <div key={column.key} className={column.className}>
              {sortHeader(column.key, column.label())}
            </div>
          ))}
        </div>
        <div className="w-32 flex-shrink-0" />
      </div>

      {sortedTemplates.map((template) => (
        <div
          key={template.id}
          draggable
          onDragStart={(e) => {
            e.dataTransfer.setData(WORKFLOW_DND_TYPE, template.id)
            e.dataTransfer.effectAllowed = 'move'
          }}
          className="group flex items-center gap-4 rounded px-4 py-3 cursor-pointer transition-colors bg-neutral-14 hover:bg-neutral-13"
          onClick={() => onEdit?.(template.id)}
        >
          <div className="flex items-center gap-3 w-64 flex-shrink-0 min-w-0">
            <div className="w-8 h-8 rounded bg-neutral-13 flex items-center justify-center flex-shrink-0">
              <WorkflowGlyph />
            </div>
            <div className="min-w-0">
              <div className="font-mono font-bold text-sm text-neutral-1 truncate">
                {template.name}
              </div>
            </div>
          </div>
          <div className="flex items-center gap-6 flex-1 min-w-0">
            {WORKFLOW_COLUMNS.map((column) => (
              <div
                key={column.key}
                className={clsx(
                  'font-mono text-sm text-neutral-3',
                  column.className,
                )}
              >
                {cellContent(template, column, ouNames)}
              </div>
            ))}
          </div>
          <div className="w-32 flex-shrink-0 flex items-center justify-end gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
            {onRun && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onRun(template)
                }}
                className="p-1.5 text-neutral-6 hover:text-lime-6 transition-colors"
                aria-label={t('entities.workflow.table.runLabel', {
                  name: template.name,
                })}
                title={t('entities.workflow.table.runTitle')}
              >
                <RunIcon />
              </button>
            )}
            {onEdit && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onEdit(template.id)
                }}
                className="p-1.5 text-neutral-6 hover:text-neutral-2 transition-colors"
                aria-label={t('entities.workflow.table.editLabel', {
                  name: template.name,
                })}
              >
                <EditIcon />
              </button>
            )}
            {onClone && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onClone(template)
                }}
                className="p-1.5 text-neutral-6 hover:text-neutral-2 transition-colors"
                aria-label={t('entities.workflow.table.cloneLabel', {
                  name: template.name,
                })}
                title={t('entities.workflow.table.cloneTitle')}
              >
                <CloneIcon />
              </button>
            )}
            {onExport && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onExport(template)
                }}
                className="p-1.5 text-neutral-6 hover:text-neutral-2 transition-colors"
                aria-label={t('entities.workflow.table.exportLabel', {
                  name: template.name,
                })}
                title={t('entities.workflow.table.exportTitle')}
              >
                <ExportIcon />
              </button>
            )}
            {onDelete && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation()
                  onDelete(template)
                }}
                className="p-1.5 text-neutral-6 hover:text-red-6 transition-colors"
                aria-label={t('entities.workflow.table.deleteLabel', {
                  name: template.name,
                })}
              >
                <TrashIcon />
              </button>
            )}
          </div>
        </div>
      ))}
    </div>
  )
}
