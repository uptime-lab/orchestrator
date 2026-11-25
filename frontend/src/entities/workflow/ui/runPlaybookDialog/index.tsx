import { Dialog, DialogPanel, DialogTitle } from '@headlessui/react'
import clsx from 'clsx'
import { FC, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { bootWarnings, graphToFlow } from '@/pages/workflowEditor/graphUtils'

import { nodesModel } from '@/entities/node'
import { useNodes } from '@/entities/node/hooks.ts'
import { nodeDisplayName, nodeStatusText } from '@/entities/node/utils.ts'
import { useOUs } from '@/entities/ou'
import { usePreferHostnames } from '@/entities/user/preferences'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { apiWorkflows } from '../../api'
import type { WorkflowTemplate } from '../../api/types'
import { isWorkflowActive, isWorkflowAvailableForOU } from '../../lib/scope'
import { useWorkflowBlocks } from '../../model'
import {
  RunConsequencesNotice,
  useRunConsequences,
} from './RunConsequencesNotice'

export type RunPlaybookDialogProps = {
  template: WorkflowTemplate | null
  onClose: () => void
  onStarted: (executionId: string) => void
}

// RunPlaybookDialog is the inverse of the node sidebar's run picker: there an
// operator holds a node and picks a playbook, here they hold a playbook and
// pick nodes. The Playbooks page had no run affordance at all — the editor even
// returns the author to that page after saving, i.e. to the one screen where
// the thing they just built could not be used.
//
// Scope is the same rule the node-side picker applies, evaluated per node:
// a playbook runs on its own OU, a child OU, or anywhere when unscoped.
export const RunPlaybookDialog: FC<RunPlaybookDialogProps> = ({
  template,
  onClose,
  onStarted,
}) => {
  const { t } = useTranslation()
  const preferHostnames = usePreferHostnames()
  const { nodes } = useNodes()
  const { ous } = useOUs()
  const { blocks } = useWorkflowBlocks()
  const [selected, setSelected] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [loadingNodes, setLoadingNodes] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // The node store is filled by the Nodes page, so on a direct visit to
  // /workflows it is empty and every playbook would look out of scope. Fetch on
  // open — it is also the only way the statuses shown here are current.
  useEffect(() => {
    if (!template) return
    let cancelled = false
    setLoadingNodes(true)
    nodesModel
      .fetchNodesFx()
      .catch(() => undefined)
      .finally(() => {
        if (!cancelled) setLoadingNodes(false)
      })
    return () => {
      cancelled = true
    }
  }, [template])

  const eligible = useMemo(() => {
    if (!template) return []
    return nodes.filter((node) =>
      isWorkflowAvailableForOU(template, node.ou_id, ous),
    )
  }, [nodes, ous, template])

  const isDraft = !!template && !isWorkflowActive(template)

  const blocksByType = useMemo(() => {
    const map: Record<string, (typeof blocks)[number]> = {}
    blocks.forEach((block) => {
      map[block.type] = block
    })
    return map
  }, [blocks])

  // The author of a playbook is not the person who picks the node, so the
  // boot-state check is re-asked HERE with the node's actual state as the
  // starting belief — the same walk the editor runs, seeded instead of blank.
  const bootMismatch = useMemo(() => {
    const graph = template?.graph
    if (!graph || blocks.length === 0) return {}
    const flow = graphToFlow(graph)
    const out: Record<string, string> = {}
    eligible.forEach((node) => {
      const mode = node.system_info?.hardware?.boot?.recovery_bootstrap
        ? 'recovery'
        : node.status === 'online'
          ? 'managed_os'
          : undefined
      if (!mode) return
      const issues = bootWarnings(flow.nodes, flow.edges, blocksByType, mode)
      if (issues.length > 0) out[node.id] = issues[0]
    })
    return out
  }, [template, blocks, blocksByType, eligible])

  // What this run will DO, and whether it may start: the same hook and notice
  // the node panel's run picker uses.
  const { consequences, gate, acknowledged, setAcknowledged } =
    useRunConsequences(template)

  const toggle = (nodeId: string) => {
    setSelected((prev) =>
      prev.includes(nodeId)
        ? prev.filter((id) => id !== nodeId)
        : [...prev, nodeId],
    )
  }

  const handleStart = async () => {
    if (!template || selected.length === 0) return
    setError(null)
    setBusy(true)
    try {
      const execution = await apiWorkflows.postStartWorkflowExecution({
        template_id: template.id,
        node_ids: selected,
      })
      setSelected([])
      onStarted(execution.id)
    } catch (err) {
      setError(
        extractApiErrorMessage(
          err,
          t('entities.workflow.runPlaybookDialog.startError'),
        ),
      )
    } finally {
      setBusy(false)
    }
  }

  const handleClose = () => {
    setSelected([])
    setAcknowledged(false)
    setError(null)
    onClose()
  }

  return (
    <Dialog open={!!template} onClose={handleClose} className="relative z-50">
      <div className="fixed inset-0 bg-neutral-15/80" aria-hidden="true" />
      <div className="fixed inset-0 flex items-center justify-center p-4">
        <DialogPanel className="w-full max-w-lg bg-neutral-14 border border-neutral-11 rounded p-5 flex flex-col gap-4 shadow-lg">
          <DialogTitle className="text-sm font-bold uppercase text-neutral-1">
            {t('entities.workflow.runPlaybookDialog.title', {
              name: template?.name,
            })}
          </DialogTitle>

          {error && (
            <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-3 py-2 rounded text-sm">
              {error}
            </div>
          )}

          {isDraft ? (
            // Say what a draft costs, where the operator meets it. The run
            // pickers filter drafts out silently, which is how "there is one
            // and it is a draft" reads as "there are none".
            <div className="text-sm text-neutral-4">
              {t('entities.workflow.runPlaybookDialog.draftIntro')}{' '}
              <span className="text-neutral-2">
                {t('entities.workflow.runPlaybookDialog.draftWord')}
              </span>
              {t('entities.workflow.runPlaybookDialog.draftSuffix')}
            </div>
          ) : loadingNodes && eligible.length === 0 ? (
            <div className="text-sm text-neutral-6">
              {t('entities.workflow.runPlaybookDialog.loadingNodes')}
            </div>
          ) : eligible.length === 0 ? (
            <div className="text-sm text-neutral-4">
              {t('entities.workflow.runPlaybookDialog.noNodes')}
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              <div className="text-xs uppercase text-neutral-7">
                {t('entities.workflow.runPlaybookDialog.nodesHeader', {
                  count: selected.length,
                })}
              </div>
              <div className="max-h-64 overflow-y-auto flex flex-col gap-1 border border-neutral-11 rounded p-2">
                {eligible.map((node) => (
                  <label
                    key={node.id}
                    className="flex items-center gap-2 px-1 py-1 rounded hover:bg-neutral-13 cursor-pointer"
                  >
                    <input
                      type="checkbox"
                      checked={selected.includes(node.id)}
                      onChange={() => toggle(node.id)}
                    />
                    <span className="text-sm text-neutral-2 truncate">
                      {nodeDisplayName(node, preferHostnames) || node.id}
                    </span>
                    <span className="text-xs ml-auto flex items-center gap-1.5">
                      {node.system_info?.hardware?.boot?.recovery_bootstrap && (
                        <span className="text-purple-4 uppercase text-[10px]">
                          {t('entities.workflow.runPlaybookDialog.recovery')}
                        </span>
                      )}
                      {bootMismatch[node.id] && (
                        <span
                          className="text-yellow-5 text-[10px] uppercase"
                          title={bootMismatch[node.id]}
                        >
                          {t(
                            'entities.workflow.runPlaybookDialog.checkBootMode',
                          )}
                        </span>
                      )}
                      <span
                        className={clsx(
                          node.status === 'online'
                            ? 'text-green-6'
                            : 'text-neutral-6',
                        )}
                      >
                        {nodeStatusText(node.status, null)}
                      </span>
                    </span>
                  </label>
                ))}
              </div>
              {/* An offline node is not refused here: a queueable block waits
                  for it, and everything else fails honestly and visibly. */}
            </div>
          )}

          {!isDraft && (
            <RunConsequencesNotice
              consequences={consequences}
              gate={gate}
              acknowledged={acknowledged}
              onAcknowledgedChange={setAcknowledged}
              disabled={busy}
            />
          )}

          <div className="flex gap-2 justify-end">
            <Button
              size="md"
              kind="secondary"
              onClick={handleClose}
              disabled={busy}
            >
              {t('entities.workflow.runPlaybookDialog.cancel')}
            </Button>
            {!isDraft && (
              <Button
                size="md"
                kind="primary"
                onClick={handleStart}
                disabled={busy || selected.length === 0 || gate !== 'ready'}
              >
                {busy
                  ? t('entities.workflow.runPlaybookDialog.starting')
                  : t('entities.workflow.runPlaybookDialog.runOn', {
                      count: selected.length,
                    })}
              </Button>
            )}
          </div>
        </DialogPanel>
      </div>
    </Dialog>
  )
}
