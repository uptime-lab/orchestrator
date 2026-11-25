import clsx from 'clsx'
import { createLocator, Locator, Mark } from 'create-locator'
import { t } from 'i18next'
import { FC } from 'react'

import type { WorkflowExecution } from '@/entities/workflow/api/types'
import { useWorkflowTemplates } from '@/entities/workflow/hooks'

export type WorkflowExecutionCardLocator = Locator<{
  name: { name: string }
}>

export type WorkflowExecutionCardProps = {
  id: string
  execution: WorkflowExecution
  selected?: boolean
  onClick?: (executionId: string) => unknown
} & Mark<WorkflowExecutionCardLocator>

const getStatusColor = (status: WorkflowExecution['status']) => {
  switch (status) {
    case 'completed':
      return 'text-green-6 bg-green-1/10 border-green-6/20'
    case 'failed':
      return 'text-red-6 bg-red-1/10 border-red-6/20'
    case 'partially_failed':
      return 'text-yellow-6 bg-yellow-1/10 border-yellow-6/20'
    case 'running':
      return 'text-blue-6 bg-blue-1/10 border-blue-6/20'
    case 'pending':
      return 'text-neutral-6 bg-neutral-12 border-neutral-11'
    default:
      return 'text-neutral-6 bg-neutral-12 border-neutral-11'
  }
}

const getStatusLabel = (status: WorkflowExecution['status']) => {
  switch (status) {
    case 'completed':
      return t('entities.workflow.executionCard.statusCompleted')
    case 'failed':
      return t('entities.workflow.executionCard.statusFailed')
    case 'partially_failed':
      return t('entities.workflow.executionCard.statusPartiallyFailed')
    case 'running':
      return t('entities.workflow.executionCard.statusRunning')
    case 'pending':
      return t('entities.workflow.executionCard.statusPending')
    default:
      // An unrecognised status is the server's own word — shown verbatim
      // rather than paraphrased into something the API never said.
      return status
  }
}

export const WorkflowExecutionCard: FC<WorkflowExecutionCardProps> = ({
  id,
  execution,
  selected,
  onClick,
  ...rest
}) => {
  const locator = createLocator(rest)
  const { templates } = useWorkflowTemplates()
  const template = templates.find((t) => t.id === execution.template_id)
  // The run's pinned snapshot names deleted playbooks
  const playbookName =
    template?.name ||
    execution.template_snapshot?.name ||
    t('entities.workflow.executionCard.deletedPlaybook')

  const handleClick = () => {
    onClick?.(id)
  }

  const nodeCount = execution.node_count ?? execution.nodes?.length ?? 0

  return (
    <div
      className={clsx(
        'flex items-center gap-4 pl-0 pr-4 py-3',
        'select-none rounded transition-colors',
        selected ? 'bg-neutral-12' : 'bg-neutral-14 hover:bg-neutral-13',
      )}
      {...locator()}
    >
      <button
        type="button"
        className="flex items-center gap-4 flex-1 min-w-0 px-4"
        onClick={handleClick}
      >
        <div className="flex-1 flex flex-col items-start min-w-0">
          <div
            className="text-sm font-medium text-neutral-1 truncate"
            {...locator.name({ name: playbookName })}
          >
            {playbookName}
          </div>
          <div className="flex items-center gap-2 mt-1">
            <span
              className={clsx(
                'text-xs px-2 py-0.5 rounded border',
                getStatusColor(execution.status),
              )}
            >
              {getStatusLabel(execution.status)}
            </span>
            {nodeCount > 0 && (
              <span className="text-xs text-neutral-6">
                {t('entities.workflow.executionCard.nodeCount', {
                  count: nodeCount,
                })}
              </span>
            )}
          </div>
        </div>
        <div className="flex items-center gap-2 flex-none">
          <div className="text-xs text-neutral-6">
            {new Date(execution.created_at).toLocaleString()}
          </div>
          <div className="text-neutral-6 text-sm">{selected ? '▼' : '▶'}</div>
        </div>
      </button>
    </div>
  )
}
