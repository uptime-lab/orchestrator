import { createLocator, Locator, Mark } from 'create-locator'
import { useTranslation } from 'react-i18next'

import { useWorkflowExecutions } from '@/entities/workflow/hooks'

import {
  WorkflowExecutionCard,
  WorkflowExecutionCardLocator,
} from './workflowExecutionCard'

export type WorkflowExecutionListLocator = Locator<{
  card: WorkflowExecutionCardLocator
}>

export type WorkflowExecutionListProps = {
  selectedExecutionId?: string
  onExecutionClick?: (executionId: string) => unknown
} & Partial<Mark<WorkflowExecutionListLocator>>

// Renders the current page of the server-paginated executions store; filtering
// (playbook/OU) happens server-side via the list query params.
export const WorkflowExecutionList = ({
  onExecutionClick,
  selectedExecutionId,
  ...rest
}: WorkflowExecutionListProps) => {
  const { t } = useTranslation()
  const { executions } = useWorkflowExecutions()
  const locator = createLocator(rest as Mark<WorkflowExecutionListLocator>)

  const visibleExecutions = executions

  if (visibleExecutions.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-neutral-7">
        <p className="text-sm">{t('entities.workflow.executionList.empty')}</p>
        <p className="text-xs mt-2">
          {t('entities.workflow.executionList.emptyHint')}
        </p>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-2" {...locator()}>
      {visibleExecutions.map((execution) => (
        <WorkflowExecutionCard
          key={execution.id}
          id={execution.id}
          execution={execution}
          selected={selectedExecutionId === execution.id}
          onClick={onExecutionClick}
          {...locator.card()}
        />
      ))}
    </div>
  )
}
