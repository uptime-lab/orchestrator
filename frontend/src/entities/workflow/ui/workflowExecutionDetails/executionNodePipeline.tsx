import clsx from 'clsx'
import { t } from 'i18next'
import { FC } from 'react'

import type { NodeCommand } from '@/entities/node/api/types'
import type {
  WorkflowExecutionNode,
  WorkflowStep,
  WorkflowTemplate,
} from '@/entities/workflow/api/types'
import {
  getWorkflowBlockDisplayName,
  useWorkflowBlocks,
} from '@/entities/workflow/model'

type ExecutionNodePipelineProps = {
  executionNode: WorkflowExecutionNode
  template: WorkflowTemplate
  commands?: Record<string, NodeCommand> // Map of command_id -> NodeCommand
}

const getStepStatus = (
  stepIndex: number,
  executionNode: WorkflowExecutionNode,
  command?: NodeCommand,
): 'pending' | 'running' | 'success' | 'failed' => {
  // If step hasn't started yet (current_step_index is -1 or less than stepIndex)
  if (executionNode.current_step_index < stepIndex) {
    return 'pending'
  }

  // If this is the current step
  if (executionNode.current_step_index === stepIndex) {
    // If node failed and this is the current step, the step failed
    if (executionNode.status === 'failed') {
      return 'failed'
    }

    if (command) {
      if (command.status === 'success') {
        // Command succeeded but node hasn't moved to next step yet
        return 'success'
      }
      if (command.status === 'failed') {
        return 'failed'
      }
      if (
        command.status === 'in_progress' ||
        command.status === 'sent' ||
        command.status === 'registered'
      ) {
        return 'running'
      }
    }
    // If no command but this is current step, it's running
    return 'running'
  }

  // If step is completed (current_step_index > stepIndex)
  // All completed steps are successful unless the node failed at this step
  // But if we're past this step, it must have succeeded
  return 'success'
}

const StepIcon: FC<{
  status: 'pending' | 'running' | 'success' | 'failed'
}> = ({ status }) => {
  switch (status) {
    case 'pending':
      return (
        <div className="w-5 h-5 rounded-full border-2 border-neutral-8 bg-neutral-13" />
      )
    case 'running':
      return (
        <div className="relative w-5 h-5">
          <div className="absolute inset-0 rounded-full border-2 border-blue-6 border-t-transparent animate-spin" />
        </div>
      )
    case 'success':
      return (
        <div className="w-5 h-5 rounded-full bg-green-6 flex items-center justify-center">
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
          >
            <path
              d="M2 6L5 9L10 2"
              stroke="white"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </div>
      )
    case 'failed':
      return (
        <div className="w-5 h-5 rounded-full bg-red-6 flex items-center justify-center">
          <svg
            width="12"
            height="12"
            viewBox="0 0 12 12"
            fill="none"
            xmlns="http://www.w3.org/2000/svg"
          >
            <path
              d="M3 3L9 9M9 3L3 9"
              stroke="white"
              strokeWidth="2"
              strokeLinecap="round"
            />
          </svg>
        </div>
      )
  }
}

const StepLine: FC<{
  status: 'pending' | 'running' | 'success' | 'failed'
}> = ({ status }) => {
  const isCompleted = status === 'success'
  return (
    <div
      className={clsx(
        'w-0.5 h-6 ml-[10px]',
        isCompleted ? 'bg-green-6' : 'bg-neutral-11',
      )}
    />
  )
}

const formatDuration = (start: string, end?: string): string => {
  if (!end) return t('entities.workflow.executionNodePipeline.running')
  const startTime = new Date(start).getTime()
  const endTime = new Date(end).getTime()
  const duration = endTime - startTime
  const seconds = Math.floor(duration / 1000)
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const remainingSeconds = seconds % 60
  return `${minutes}m ${remainingSeconds}s`
}

export const ExecutionNodePipeline: FC<ExecutionNodePipelineProps> = ({
  executionNode,
  template,
  commands = {},
}) => {
  const { blocksBySlug } = useWorkflowBlocks()
  const sortedSteps = [...template.steps].sort((a, b) => a.order - b.order)

  return (
    <div className="flex flex-col">
      {sortedSteps.map((step: WorkflowStep, index: number) => {
        // Find command for this step
        // For current step, use current_command_id
        // For past steps, try to find a matching command by slug
        let command: NodeCommand | undefined
        if (
          executionNode.current_step_index === index &&
          executionNode.current_command_id
        ) {
          command = commands[executionNode.current_command_id]
        } else if (executionNode.current_step_index > index) {
          // For completed steps, try to find a command matching this step's slug
          // We'll take the most recent one that matches
          const matchingCommands = Object.values(commands).filter(
            (cmd) =>
              cmd.slug === step.command.slug &&
              cmd.node_id === executionNode.node_id,
          )
          if (matchingCommands.length > 0) {
            // Sort by created_at descending to get most recent
            matchingCommands.sort(
              (a, b) =>
                new Date(b.created_at).getTime() -
                new Date(a.created_at).getTime(),
            )
            command = matchingCommands[0]
          }
        }

        const stepStatus = getStepStatus(index, executionNode, command)

        const stepName =
          step.name ||
          getWorkflowBlockDisplayName(blocksBySlug, step.command.slug)

        return (
          // A pipeline step's identity IS its position: the same slug can
          // appear at several steps and the list is re-rendered whole.
          // eslint-disable-next-line react/no-array-index-key
          <div key={index} className="flex items-start gap-3">
            {/* Step icon and connecting line */}
            <div className="flex flex-col items-center">
              <StepIcon status={stepStatus} />
              {index < sortedSteps.length - 1 && (
                <StepLine status={stepStatus} />
              )}
            </div>

            {/* Step content */}
            <div className="flex-1 pb-4">
              <div className="flex items-center gap-2 mb-1">
                <span className="text-sm font-medium text-neutral-2">
                  {stepName}
                </span>
                <span className="text-xs text-neutral-6 font-mono">
                  {step.command.slug}
                </span>
                {command && command.status === 'in_progress' && (
                  <span className="text-xs text-blue-6 font-medium">
                    {command.progress}%
                  </span>
                )}
              </div>

              {/* Progress bar for in-progress steps */}
              {stepStatus === 'running' && command && command.progress > 0 && (
                <div className="w-full h-1 bg-neutral-12 rounded-full overflow-hidden mb-2">
                  <div
                    className="h-full bg-blue-6 transition-all duration-300"
                    style={{ width: `${command.progress}%` }}
                  />
                </div>
              )}

              {/* Error message */}
              {stepStatus === 'failed' && (
                <div className="text-xs text-red-6 bg-red-1/10 border border-red-6/20 rounded p-2 mt-1">
                  {executionNode.error_message ||
                    command?.details ||
                    t('entities.workflow.executionNodePipeline.stepFailed')}
                </div>
              )}

              {/* Step description */}
              {step.description && (
                <div className="text-xs text-neutral-6 mt-1">
                  {step.description}
                </div>
              )}

              {/* Timing */}
              {command && (
                <div className="text-xs text-neutral-7 mt-1">
                  {formatDuration(command.created_at, command.updated_at)}
                </div>
              )}
            </div>
          </div>
        )
      })}
    </div>
  )
}
