import { useNavigate } from '@tanstack/react-router'
import { isAxiosError } from 'axios'
import clsx from 'clsx'
import type { TFunction } from 'i18next'
import { FC, useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { RunGraphView } from '@/pages/workflowEditor/runGraphView'

import { nodesHooks } from '@/entities/node'
import type { NodeCommand } from '@/entities/node/api/types'
import { NodeStatusIcon } from '@/entities/node/ui/nodeList'
import { Actions, useCan, useCanAnyScope } from '@/entities/permissions/hooks'
import { userCommonSocketModel } from '@/entities/userCommonSocket'
import {
  apiWorkflows,
  fetchWorkflowExecutionFx,
  fetchWorkflowExecutionNodesFx,
  fetchWorkflowTemplateFx,
  getWorkflowBlockDisplayName,
  useWorkflowBlocks,
  useWorkflowExecution,
  useWorkflowTemplate,
} from '@/entities/workflow'

import { SidebarEnum, SidebarProps } from '../types'

const getStatusColor = (
  status: 'pending' | 'running' | 'completed' | 'failed' | 'partially_failed',
) => {
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

const getStatusLabel = (
  t: TFunction,
  status: 'pending' | 'running' | 'completed' | 'failed' | 'partially_failed',
) => {
  switch (status) {
    case 'completed':
      return t('widgets.sidebars.workflowExecutionDetails.statusCompleted')
    case 'failed':
      return t('widgets.sidebars.workflowExecutionDetails.statusFailed')
    case 'partially_failed':
      return t(
        'widgets.sidebars.workflowExecutionDetails.statusPartiallyFailed',
      )
    case 'running':
      return t('widgets.sidebars.workflowExecutionDetails.statusRunning')
    case 'pending':
      return t('widgets.sidebars.workflowExecutionDetails.statusPending')
    default:
      return status
  }
}

// isTerminalFetchError reports whether a failed poll should STOP retrying: a
// 404 (the run is gone) or a hard network failure. Anything else is transient
// and the next tick tries again. Typed against unknown because the value comes
// from a rejected promise, where `any` let a shape change pass silently.
const isTerminalFetchError = (error: unknown): boolean => {
  const err = error as { response?: { status?: number }; code?: string }
  return err?.response?.status === 404 || err?.code === 'ERR_NETWORK'
}

export const SidebarWorkflowExecutionDetails: FC<
  SidebarProps & { executionId: string }
> = ({ executionId, onClose }) => {
  const { t } = useTranslation()
  const { execution, nodes } = useWorkflowExecution(executionId)
  const { blocks, fetchBlocks } = useWorkflowBlocks()
  // Store commands by execution node ID, then by command index
  const [commandsByNode, setCommandsByNode] = useState<
    Record<string, NodeCommand[]>
  >({})
  const [expandedGraphNodeId, setExpandedGraphNodeId] = useState<string | null>(
    null,
  )
  const [expandedOutputNodeId, setExpandedOutputNodeId] = useState<
    string | null
  >(null)
  const [cancelling, setCancelling] = useState(false)
  const [cancelError, setCancelError] = useState<string | null>(null)
  const [retrying, setRetrying] = useState(false)
  const [retryError, setRetryError] = useState<string | null>(null)
  const navigate = useNavigate()
  const fetchingRef = useRef<Set<string>>(new Set())
  const hasErrorRef = useRef(false)
  const lastExecutionIdRef = useRef<string>('')

  // Block registry provides step display names
  useEffect(() => {
    if (blocks.length === 0) {
      fetchBlocks()
    }
  }, [blocks.length, fetchBlocks])

  // Reset error state when executionId changes
  useEffect(() => {
    if (lastExecutionIdRef.current !== executionId) {
      hasErrorRef.current = false
      lastExecutionIdRef.current = executionId
    }
  }, [executionId])

  // Fetch execution and nodes on mount - only once per executionId
  useEffect(() => {
    // Don't fetch if we already have an error (e.g., 404)
    if (hasErrorRef.current) {
      return
    }

    // Use the effects directly instead of the wrapper functions
    const fetchExecution = async () => {
      try {
        await fetchWorkflowExecutionFx(executionId)
      } catch (error: unknown) {
        // Stop retrying on 404 or other errors
        if (isTerminalFetchError(error)) {
          hasErrorRef.current = true
        }
      }
    }

    const fetchNodes = async () => {
      try {
        await fetchWorkflowExecutionNodesFx(executionId)
      } catch (error: unknown) {
        // Stop retrying on 404 or other errors
        if (isTerminalFetchError(error)) {
          hasErrorRef.current = true
        }
      }
    }

    fetchExecution()
    fetchNodes()
    // Only depend on executionId — the effects are stable module-level fx.
  }, [executionId])

  // Fetch template
  const templateId = execution?.template_id
  const { template } = useWorkflowTemplate(templateId || '')
  const [templateMissing, setTemplateMissing] = useState(false)

  useEffect(() => {
    setTemplateMissing(false)
    // Per-RUN state, and the panel is re-rendered rather than remounted when
    // the operator clicks another run (the sidebar carries no key). Without
    // this, run A's "failed to cancel" renders in run B's header — attributing
    // a failure to a run it never happened on, and on a completed run that
    // shows no Cancel button at all.
    setCancelError(null)
    setCancelling(false)
    setRetryError(null)
    setRetrying(false)
  }, [executionId])

  // The run's pinned snapshot is the source of truth; without one (runs
  // started before snapshotting) try the live playbook and degrade gracefully
  // when it was deleted instead of blocking the panel.
  const snapshot = execution?.template_snapshot
  useEffect(() => {
    if (!templateId || template || snapshot) return
    fetchWorkflowTemplateFx(templateId).catch(() => setTemplateMissing(true))
  }, [templateId, template, snapshot])

  const templateView = snapshot
    ? { id: snapshot.template_id, name: snapshot.name, steps: snapshot.steps }
    : template
      ? { id: template.id, name: template.name, steps: template.steps }
      : templateMissing && templateId
        ? {
            id: templateId,
            name: t(
              'widgets.sidebars.workflowExecutionDetails.deletedPlaybook',
            ),
            steps: [],
          }
        : null

  // Get WebSocket events for real-time updates
  const nodeCommandUpdatedEvent =
    userCommonSocketModel.events.userSocketOnNodeCommandUpdated

  // Fetch commands for each execution node
  const fetchCommandsForNode = useCallback(
    async (executionNodeId: string, nodeId: string) => {
      if (fetchingRef.current.has(executionNodeId)) {
        return // Already fetching
      }

      fetchingRef.current.add(executionNodeId)
      try {
        const commands = await apiWorkflows.getExecutionNodeCommands(
          executionId,
          nodeId,
        )
        // Commands are already ordered by created_at ASC
        setCommandsByNode((prev) => ({
          ...prev,
          [executionNodeId]: commands,
        }))
      } catch {
        // The panel renders an empty command list rather than an error: the
        // run itself is still worth showing, and the next poll retries.
      } finally {
        fetchingRef.current.delete(executionNodeId)
      }
    },
    [executionId],
  )

  // Cancel is the only remedy for a run that will never settle by itself — a
  // deleted node, a block whose agent never answered.
  // Run controls are shown to roles that may use them: cancelling needs the
  // run right account-wide (as the route checks it), a retry starts a run.
  const check = useCan()
  const checkAnyScope = useCanAnyScope()
  const canCancelRun = check(Actions.PlaybookRun)
  const canStartRun = checkAnyScope(Actions.PlaybookRun)

  const handleCancel = useCallback(async () => {
    setCancelError(null)
    setCancelling(true)
    try {
      await apiWorkflows.postCancelWorkflowExecution(executionId)
      await fetchWorkflowExecutionFx(executionId)
      await fetchWorkflowExecutionNodesFx(executionId)
    } catch (error) {
      const message =
        isAxiosError(error) && typeof error.response?.data?.message === 'string'
          ? error.response.data.message
          : t('widgets.sidebars.workflowExecutionDetails.cancelFailed')
      setCancelError(message)
    } finally {
      setCancelling(false)
    }
  }, [executionId, t])

  // Retry starts a NEW run of the same playbook against only the nodes that
  // failed here, instead of re-selecting them on the Nodes page by hand. It
  // runs the CURRENT playbook, not this run's pinned
  // snapshot — the snapshot exists to keep an in-flight run stable, while a
  // retry is a fresh decision that should pick up whatever fix was just made.
  const handleRetryFailed = useCallback(
    async (templateId: string, failedNodeIds: string[]) => {
      setRetryError(null)
      setRetrying(true)
      try {
        const newExecution = await apiWorkflows.postStartWorkflowExecution({
          template_id: templateId,
          node_ids: failedNodeIds,
        })
        navigate({
          to: '.',
          search: {
            sidebar: SidebarEnum.WorkflowExecutionDetails,
            executionId: newExecution.id,
          },
        })
      } catch (error) {
        const message =
          isAxiosError(error) &&
          typeof error.response?.data?.message === 'string'
            ? error.response.data.message
            : t('widgets.sidebars.workflowExecutionDetails.retryStartFailed')
        setRetryError(message)
        setRetrying(false)
      }
      // No setRetrying(false) on success: the navigate above swaps the panel
      // to the new run, and the executionId reset effect clears the state.
    },
    [navigate, t],
  )

  // Fetch commands when nodes change
  useEffect(() => {
    if (!nodes || nodes.length === 0) {
      setCommandsByNode({})
      return
    }

    nodes.forEach((node) => {
      fetchCommandsForNode(node.id, node.node_id)
    })
  }, [nodes, fetchCommandsForNode])

  // Update commands when WebSocket event is received
  useEffect(() => {
    const unsubscribe = nodeCommandUpdatedEvent.watch(({ payload }) => {
      // Check if this command belongs to any of our execution nodes
      if (payload.workflow_execution_node_id) {
        const executionNode = nodes?.find(
          (node) => node.id === payload.workflow_execution_node_id,
        )
        if (executionNode) {
          setCommandsByNode((prev) => {
            const nodeCommands = prev[executionNode.id] || []
            const commandIndex = nodeCommands.findIndex(
              (cmd) => cmd.id === payload.id,
            )
            if (commandIndex >= 0) {
              // Update existing command
              const updated = [...nodeCommands]
              updated[commandIndex] = payload
              return { ...prev, [executionNode.id]: updated }
            } else {
              // Add new command (shouldn't happen often, but handle it)
              return { ...prev, [executionNode.id]: [...nodeCommands, payload] }
            }
          })
        }
      }
    })
    return () => unsubscribe()
  }, [nodeCommandUpdatedEvent, nodes])

  // Chevron close header (same control as the node/OS panels), rendered in
  // every state so the panel can always be dismissed.
  const panelHeader = (
    <div className="flex items-center gap-3">
      <button
        type="button"
        onClick={onClose}
        aria-label={t('widgets.sidebars.workflowExecutionDetails.closePanel')}
        title={t('widgets.sidebars.workflowExecutionDetails.closePanel')}
        className="flex items-center justify-center w-6 h-6 rounded border border-neutral-11 text-neutral-6 hover:text-neutral-2 hover:border-neutral-9 transition-colors flex-shrink-0"
      >
        <svg
          className="w-3.5 h-3.5"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M9 5l7 7-7 7"
          />
        </svg>
      </button>
      <h2 className="text-lg font-bold uppercase">
        {t('widgets.sidebars.workflowExecutionDetails.title')}
      </h2>
    </div>
  )

  if (!execution) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        {panelHeader}
        <div className="text-neutral-4">
          {t('widgets.sidebars.workflowExecutionDetails.loadingRun')}
        </div>
      </div>
    )
  }

  if (!templateView) {
    return (
      <div className="px-5 pt-4 pb-5 flex flex-col gap-6">
        {panelHeader}
        <div className="text-neutral-4">
          {t('widgets.sidebars.workflowExecutionDetails.loadingPlaybook')}
        </div>
      </div>
    )
  }

  // "Active" is the same rule the backend caps on: no completed_at. A
  // partially_failed run with a live node still counts, and is exactly the
  // shape someone needs to stop.
  const isRunActive = !execution.completed_at

  return (
    <div className="px-5 pt-4 pb-5 flex flex-col gap-6 h-full overflow-y-auto">
      {panelHeader}

      {/* Execution header */}
      <div className="flex flex-col gap-2">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-neutral-2">
            {templateView.name}
          </span>
          <span
            className={clsx(
              'text-xs px-2 py-0.5 rounded border',
              getStatusColor(execution.status),
            )}
          >
            {getStatusLabel(t, execution.status)}
          </span>
        </div>
        <div className="text-xs text-neutral-6">
          {t('widgets.sidebars.workflowExecutionDetails.startedAt', {
            value: execution.started_at
              ? new Date(execution.started_at).toLocaleString()
              : t('widgets.sidebars.workflowExecutionDetails.notStarted'),
          })}
        </div>
        {execution.completed_at && (
          <div className="text-xs text-neutral-6">
            {t('widgets.sidebars.workflowExecutionDetails.completedAt', {
              value: new Date(execution.completed_at).toLocaleString(),
            })}
          </div>
        )}
        {templateMissing && (
          <div className="text-xs text-orange-6">
            {t('widgets.sidebars.workflowExecutionDetails.playbookDeleted')}
          </div>
        )}
        {isRunActive && canCancelRun && (
          <div className="flex items-center gap-2 pt-1">
            <button
              type="button"
              onClick={handleCancel}
              disabled={cancelling}
              className="text-xs px-2 py-1 rounded border border-red-6/40 text-red-6 hover:bg-red-1/10 disabled:opacity-50 transition-colors"
            >
              {cancelling
                ? t('widgets.sidebars.workflowExecutionDetails.cancelling')
                : t('widgets.sidebars.workflowExecutionDetails.cancelRun')}
            </button>
            <span className="text-xs text-neutral-6">
              {t('widgets.sidebars.workflowExecutionDetails.cancelHint')}
            </span>
          </div>
        )}
        {cancelError && <div className="text-xs text-red-6">{cancelError}</div>}
        {(() => {
          // Finished run with failures and a live playbook: offer a targeted
          // retry. Hidden while the run is active (its nodes are not final)
          // and when the playbook was deleted (nothing to start).
          const failedNodeIds = (nodes ?? [])
            .filter((n) => n.status === 'failed')
            .map((n) => n.node_id)
          if (
            !canStartRun ||
            isRunActive ||
            templateMissing ||
            failedNodeIds.length === 0 ||
            !execution.template_id
          ) {
            return null
          }
          return (
            <div className="flex items-center gap-2 pt-1">
              <button
                type="button"
                onClick={() =>
                  handleRetryFailed(execution.template_id, failedNodeIds)
                }
                disabled={retrying}
                className="text-xs px-2 py-1 rounded border border-lime-6/40 text-lime-6 hover:bg-lime-1/10 disabled:opacity-50 transition-colors"
              >
                {retrying
                  ? t('widgets.sidebars.workflowExecutionDetails.starting')
                  : t(
                      'widgets.sidebars.workflowExecutionDetails.retryFailedNodes',
                      { count: failedNodeIds.length },
                    )}
              </button>
              <span className="text-xs text-neutral-6">
                {t('widgets.sidebars.workflowExecutionDetails.retryHint')}
              </span>
            </div>
          )
        })()}
        {retryError && <div className="text-xs text-red-6">{retryError}</div>}
      </div>

      {/* Per-node pipelines */}
      <div className="flex flex-col">
        <h3 className="text-sm font-medium uppercase text-neutral-7 mb-2">
          {t('widgets.sidebars.workflowExecutionDetails.nodesHeading', {
            value: nodes.length,
          })}
        </h3>
        {nodes.length === 0 ? (
          <div className="text-sm text-neutral-6">
            {t('widgets.sidebars.workflowExecutionDetails.noNodes')}
          </div>
        ) : (
          nodes.map((executionNode) => {
            const showGraph = expandedGraphNodeId === executionNode.id
            const showOutput = expandedOutputNodeId === executionNode.id
            const snapshotGraph = execution.template_snapshot?.graph
            const nodeCommands = commandsByNode[executionNode.id] || []
            const hasOutput = nodeCommands.some(
              (cmd) =>
                cmd.details ||
                (cmd.result && Object.keys(cmd.result).length > 0),
            )
            return (
              <div key={executionNode.id}>
                <ExecutionNodeRow
                  executionNode={executionNode}
                  template={templateView}
                  commands={nodeCommands}
                />
                {/* Why it failed. Without error_message a failed run says only
                    "Failed" and the operator has nowhere to look next. */}
                {executionNode.error_message && (
                  <div className="pl-4 pb-2 text-xs text-red-6 break-words">
                    {executionNode.error_message}
                  </div>
                )}
                {hasOutput && (
                  <div className="pl-4 pb-2">
                    <button
                      type="button"
                      onClick={() =>
                        setExpandedOutputNodeId(
                          showOutput ? null : executionNode.id,
                        )
                      }
                      className="text-xs text-neutral-6 hover:text-neutral-3 transition-colors"
                    >
                      {showOutput
                        ? t(
                            'widgets.sidebars.workflowExecutionDetails.hideOutput',
                          )
                        : t(
                            'widgets.sidebars.workflowExecutionDetails.showOutput',
                          )}
                    </button>
                    {showOutput && (
                      <CommandOutputList commands={nodeCommands} />
                    )}
                  </div>
                )}
                {snapshotGraph && (
                  <div className="pl-4 pb-2">
                    <button
                      type="button"
                      onClick={() =>
                        setExpandedGraphNodeId(
                          showGraph ? null : executionNode.id,
                        )
                      }
                      className="text-xs text-neutral-6 hover:text-neutral-3 transition-colors"
                    >
                      {showGraph
                        ? t(
                            'widgets.sidebars.workflowExecutionDetails.hideGraph',
                          )
                        : t(
                            'widgets.sidebars.workflowExecutionDetails.showGraph',
                          )}
                    </button>
                    {showGraph && (
                      <div className="mt-2">
                        <RunGraphView
                          graph={snapshotGraph}
                          graphState={executionNode.graph_state || {}}
                        />
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}

// CommandOutputList shows what each dispatched command actually reported.
//
// The panel already fetched every per-node command and used only `status` and
// `progress`, so run_script's stdout/exit_code — collected into
// node_commands.result — reached no operator anywhere in the product. Values
// arrive placeholder-redacted for callers without secret.read_value; the keys
// survive either way, so "there was output you may not read" is distinguishable
// from "there was none".
const CommandOutputList: FC<{ commands: NodeCommand[] }> = ({ commands }) => {
  const { blocksBySlug } = useWorkflowBlocks()
  return (
    <div className="mt-2 flex flex-col gap-2">
      {commands.map((command) => {
        const resultKeys = command.result ? Object.keys(command.result) : []
        if (!command.details && resultKeys.length === 0) return null
        return (
          <div
            key={command.id}
            className="rounded border border-neutral-11 bg-neutral-14 px-2 py-1.5 flex flex-col gap-1"
          >
            <div className="flex items-center gap-2">
              <span className="text-xs text-neutral-2">
                {getWorkflowBlockDisplayName(blocksBySlug, command.slug)}
              </span>
              <span
                className={clsx(
                  'text-xs font-medium',
                  command.status === 'success' && 'text-green-6',
                  command.status === 'failed' && 'text-red-6',
                  command.status !== 'success' &&
                    command.status !== 'failed' &&
                    'text-neutral-6',
                )}
              >
                {command.status}
              </span>
            </div>
            {command.details && (
              <div className="text-xs text-neutral-4 break-words whitespace-pre-wrap">
                {command.details}
              </div>
            )}
            {resultKeys.map((key) => (
              <div key={key} className="flex flex-col gap-0.5">
                <div className="text-[10px] uppercase text-neutral-7">
                  {key}
                </div>
                <pre className="text-xs text-neutral-3 font-mono whitespace-pre-wrap break-words max-h-48 overflow-y-auto m-0">
                  {command.result?.[key]}
                </pre>
              </div>
            ))}
          </div>
        )
      })}
    </div>
  )
}

type ExecutionNodeRowProps = {
  executionNode: {
    id: string
    node_id: string
    status: 'pending' | 'running' | 'completed' | 'failed'
    current_step_index: number
    current_command_id?: string
    error_message?: string
  }
  template: {
    id: string
    name: string
    steps: Array<{
      order: number
      command: { slug: string; payload: Record<string, unknown> }
      name?: string
      description?: string
    }>
  }
  commands: NodeCommand[] // Commands ordered by created_at (index = step index)
}

const getStepStatusFromCommand = (
  command: NodeCommand | undefined,
  executionNode: ExecutionNodeRowProps['executionNode'],
  stepIndex: number,
): 'pending' | 'running' | 'success' | 'failed' => {
  // If execution node is completed, all steps are success
  if (executionNode.status === 'completed') {
    return 'success'
  }

  // If execution node failed, check if this step failed
  if (executionNode.status === 'failed') {
    if (command?.status === 'failed') {
      return 'failed'
    }
    // If we're past the failed step, assume success
    if (stepIndex < executionNode.current_step_index) {
      return 'success'
    }
    return 'failed'
  }

  // No command yet = pending
  if (!command) {
    return stepIndex <= executionNode.current_step_index ? 'pending' : 'pending'
  }

  // Map command status to step status
  switch (command.status) {
    case 'success':
      return 'success'
    case 'failed':
      return 'failed'
    case 'in_progress':
    case 'sent':
    case 'registered':
      return 'running'
    default:
      return 'pending'
  }
}

const ExecutionNodeRow: FC<ExecutionNodeRowProps> = ({
  executionNode,
  template,
  commands,
}) => {
  const { t } = useTranslation()
  const { node } = nodesHooks.useNode({ nodeId: executionNode.node_id })
  const { blocksBySlug } = useWorkflowBlocks()
  const nodeName = node?.name || node?.hostname || executionNode.node_id
  const nodeIp = node?.ipv4 || ''
  const nodeStatus = node?.status || 'unreachable'

  const sortedSteps = [...template.steps].sort((a, b) => a.order - b.order)

  const [hoveredStepIndex, setHoveredStepIndex] = useState<number | null>(null)
  const [tooltipPosition, setTooltipPosition] = useState<{
    top: number
    left: number
  } | null>(null)

  const handleStepMouseEnter = useCallback(
    (index: number, element: HTMLDivElement) => {
      setHoveredStepIndex(index)
      const rect = element.getBoundingClientRect()
      setTooltipPosition({
        top: rect.top - 8,
        left: rect.left + rect.width / 2,
      })
    },
    [],
  )

  const handleStepMouseLeave = useCallback(() => {
    setHoveredStepIndex(null)
    setTooltipPosition(null)
  }, [])

  return (
    <div className="flex items-center gap-4 px-2 rounded transition-colors hover:bg-neutral-13 border-b border-neutral-11">
      {/* Left: Node Status Icon */}
      <NodeStatusIcon status={nodeStatus} backgroundColor="transparent" />

      {/* Center-Left: Node Name and IP */}
      <div className="flex flex-col gap-1 min-w-0 flex-shrink-0 flex-[0_0_20%]">
        <div className="text-sm font-medium text-neutral-1 truncate">
          {nodeName}
        </div>
        {nodeIp && (
          <div className="text-xs text-neutral-4 font-mono">{nodeIp}</div>
        )}
      </div>

      {/* Center: Stage Bars with Names */}
      <div className="flex-1 flex flex-col gap-2 relative">
        {/* Progress bars */}
        <div className="flex flex-row items-end gap-2">
          {sortedSteps.map((step, index) => {
            const isCurrentStep = executionNode.current_step_index === index
            // Map command by index: commands[0] = step[0], commands[1] = step[1], etc.
            const command = commands[index]
            const stepStatus = getStepStatusFromCommand(
              command,
              executionNode,
              index,
            )

            // Get progress percentage for running commands
            const progress = command?.progress ?? 0
            const showProgress = stepStatus === 'running' && progress > 0
            const stepName =
              step.name ||
              getWorkflowBlockDisplayName(blocksBySlug, step.command.slug)

            return (
              <div
                key={step.order}
                onMouseEnter={(e) =>
                  handleStepMouseEnter(index, e.currentTarget)
                }
                onMouseLeave={handleStepMouseLeave}
                className={clsx(
                  'flex flex-col items-center gap-1',
                  isCurrentStep ? 'flex-[0_0_33.333%]' : 'flex-1',
                )}
              >
                {/* Step name above the bar - only show for active step */}
                {isCurrentStep && (
                  <div className="text-xs text-neutral-6 text-center truncate w-full">
                    {stepStatus === 'running' && progress > 0
                      ? t(
                          'widgets.sidebars.workflowExecutionDetails.stepProgress',
                          { stepName, progress },
                        )
                      : stepName}
                  </div>
                )}
                <div className="h-2 w-full rounded-[2px] overflow-hidden relative">
                  {showProgress ? (
                    // Progress bar with fill
                    <>
                      <div className="absolute inset-0 bg-neutral-8" />
                      <div
                        className="absolute inset-y-0 left-0 bg-orange-6 transition-all duration-300"
                        style={{ width: `${progress}%` }}
                      />
                    </>
                  ) : (
                    // Solid color bar
                    <div
                      className={clsx(
                        'h-full w-full',
                        stepStatus === 'success' && 'bg-green-6',
                        stepStatus === 'running' && 'bg-orange-6',
                        stepStatus === 'failed' && 'bg-red-6',
                        stepStatus === 'pending' && 'bg-neutral-8',
                      )}
                    />
                  )}
                </div>
              </div>
            )
          })}
        </div>
        {/* Tooltip */}
        {hoveredStepIndex !== null &&
          tooltipPosition &&
          (() => {
            const hoveredStep = sortedSteps[hoveredStepIndex]
            if (!hoveredStep) return null

            const hoveredCommand = commands[hoveredStepIndex]
            const hoveredStepStatus = getStepStatusFromCommand(
              hoveredCommand,
              executionNode,
              hoveredStepIndex,
            )
            const hoveredStepName =
              hoveredStep.name ||
              getWorkflowBlockDisplayName(
                blocksBySlug,
                hoveredStep.command.slug,
              )

            const statusLabels: Record<
              'pending' | 'running' | 'success' | 'failed',
              string
            > = {
              pending: t(
                'widgets.sidebars.workflowExecutionDetails.stepStatusPending',
              ),
              running: t(
                'widgets.sidebars.workflowExecutionDetails.stepStatusRunning',
              ),
              success: t(
                'widgets.sidebars.workflowExecutionDetails.stepStatusSuccess',
              ),
              failed: t(
                'widgets.sidebars.workflowExecutionDetails.stepStatusFailed',
              ),
            }

            const statusColors: Record<
              'pending' | 'running' | 'success' | 'failed',
              string
            > = {
              pending: 'text-neutral-6',
              running: 'text-orange-6',
              success: 'text-green-6',
              failed: 'text-red-6',
            }

            return (
              <div
                className="fixed z-50 pointer-events-none"
                style={{
                  top: `${tooltipPosition.top}px`,
                  left: `${tooltipPosition.left}px`,
                  transform: 'translate(-50%, -100%)',
                }}
              >
                <div className="bg-neutral-14 border border-neutral-11 rounded px-2 py-1 shadow-lg text-xs whitespace-nowrap flex items-center gap-2">
                  <span className="text-neutral-2">{hoveredStepName}</span>
                  <span
                    className={clsx(
                      'font-medium',
                      statusColors[hoveredStepStatus],
                    )}
                  >
                    {statusLabels[hoveredStepStatus]}
                  </span>
                </div>
                {/* Arrow */}
                <div className="absolute left-1/2 -bottom-1 transform -translate-x-1/2 w-0 h-0 border-l-[4px] border-r-[4px] border-t-[4px] border-l-transparent border-r-transparent border-t-neutral-11" />
              </div>
            )
          })()}
      </div>
    </div>
  )
}
