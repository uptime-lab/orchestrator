import { useNavigate } from '@tanstack/react-router'
import { FC, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SidebarEnum } from '@/widgets/sidebars/types'

import { nodesHooks, nodesModel } from '@/entities/node'
import { apiNodes } from '@/entities/node/api'
import { nodeDisplayName } from '@/entities/node/utils.ts'
import { useOUs } from '@/entities/ou'
import { usePreferHostnames } from '@/entities/user/preferences'
import {
  isWorkflowActive,
  isWorkflowAvailableForOU,
} from '@/entities/workflow/lib/scope'
import { useWorkflowTemplates } from '@/entities/workflow/model'

import { Button } from '@/shared/ui/button/button.tsx'
import { ConfirmDialog } from '@/shared/ui/confirmDialog'
import { showSuccess } from '@/shared/ui/toast'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { replacementUnitId } from './replaceNodeScope.ts'
import { SubViewHeader } from './subViewHeader.tsx'

const selectClassName =
  'flex-1 min-w-0 bg-neutral-13 rounded px-2.5 py-2 text-sm text-white outline-none transition-colors disabled:opacity-50'

// Replace a failed node with a spare one: transfers this node's custom
// attributes + hostname + OU onto the target, optionally runs a playbook there
// to re-apply the identity to external services (set_hostname, CI re-registration, ...),
// then strips this node's attributes.
export const ReplaceNodeControl: FC<{
  nodeId: string
  // Rendered inline as an Actions-tab sub-view (slides in) rather than an
  // in-place spoiler: the form shows immediately with a "← Back" header.
  embedded?: boolean
  onBack?: () => void
}> = ({ nodeId, embedded = false, onBack }) => {
  const { t } = useTranslation()
  const preferHostnames = usePreferHostnames()
  const navigate = useNavigate()
  const { nodes } = nodesHooks.useNodes()
  const { node } = nodesHooks.useNode({ nodeId })
  const { templates, fetchTemplates } = useWorkflowTemplates()
  const { ous } = useOUs()

  const [open, setOpen] = useState(embedded)
  const [targetId, setTargetId] = useState('')
  const [templateId, setTemplateId] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (open && templates.length === 0) fetchTemplates()
  }, [open, templates.length, fetchTemplates])

  // Any other node can receive the identity; online ones first.
  const targets = useMemo(
    () =>
      nodes
        .filter((n) => n.id !== nodeId)
        .slice()
        .sort((a, b) => {
          const ao = a.status === 'online' ? 0 : 1
          const bo = b.status === 'online' ? 0 : 1
          if (ao !== bo) return ao - bo
          return nodeDisplayName(a, preferHostnames).localeCompare(
            nodeDisplayName(b, preferHostnames),
          )
        }),
    [nodes, nodeId, preferHostnames],
  )

  // Playbooks available to the target in the unit it holds after the move.
  const target = targets.find((n) => n.id === targetId)
  const availableTemplates = useMemo(
    () =>
      templates.filter(
        (t) =>
          isWorkflowActive(t) &&
          isWorkflowAvailableForOU(t, replacementUnitId(node, target), ous),
      ),
    [templates, target, node, ous],
  )
  const selectedTemplate = availableTemplates.find((t) => t.id === templateId)

  const handleReplace = async () => {
    setBusy(true)
    setError(null)
    try {
      const result = await apiNodes.replaceNode(nodeId, {
        target_node_id: targetId,
        template_id: templateId || undefined,
      })
      await nodesModel.fetchNodesFx()
      setConfirmOpen(false)
      setOpen(false)
      // Say what actually carried over. The response has always reported it and
      // the control read both fields and threw them away — on an operation whose
      // entire purpose is that transfer, closing silently leaves the operator to
      // go and check by hand whether the attributes and the OU followed.
      showSuccess(
        t('widgets.sidebars.node.replaceNodeControl.replacedWith', {
          hostname: result.hostname,
        }),
        `${t('widgets.sidebars.node.replaceNodeControl.attributesTransferred', {
          count: result.attributes_transferred,
        })} ` +
          (result.ou_transferred
            ? t('widgets.sidebars.node.replaceNodeControl.ouMoved')
            : t('widgets.sidebars.node.replaceNodeControl.ouNotMoved')),
      )
      if (result.execution_id) {
        navigate({
          to: '.',
          search: {
            sidebar: SidebarEnum.WorkflowExecutionDetails,
            executionId: result.execution_id,
          },
        })
      } else if (embedded) {
        onBack?.()
      }
    } catch (err: unknown) {
      setError(
        extractApiErrorMessage(
          err,
          t('widgets.sidebars.node.replaceNodeControl.replaceError'),
        ),
      )
      setConfirmOpen(false)
    } finally {
      setBusy(false)
    }
  }

  const targetLabel = (n: (typeof targets)[number]) =>
    `${nodeDisplayName(n, preferHostnames) || n.id.slice(0, 8)}${n.status === 'online' ? '' : t('widgets.sidebars.node.replaceNodeControl.offlineSuffix')}`

  if (!open && !embedded) {
    return (
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="flex items-center gap-3 px-3 py-2 text-sm text-neutral-3 hover:bg-neutral-13 hover:text-red-6 rounded transition-colors text-left"
      >
        <svg
          className="w-4 h-4 flex-shrink-0"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M8 7h12m0 0l-4-4m4 4l-4 4M16 17H4m0 0l4 4m-4-4l4-4"
          />
        </svg>
        {t('widgets.sidebars.node.replaceNodeControl.openButton')}
      </button>
    )
  }

  return (
    <div
      className={
        embedded
          ? 'flex flex-col gap-3'
          : 'flex flex-col gap-2 px-3 py-2 rounded border border-red-6/20 bg-red-6/5'
      }
    >
      {embedded && (
        <SubViewHeader
          title={t('widgets.sidebars.node.replaceNodeControl.title')}
          onBack={() => onBack?.()}
        />
      )}
      <div className="text-xs text-neutral-5 leading-relaxed">
        {t('widgets.sidebars.node.replaceNodeControl.description')}
      </div>
      {error && (
        <div className="text-xs text-red-6 bg-red-6/10 border border-red-6/20 rounded px-2 py-1.5">
          {error}
        </div>
      )}
      <label className="text-xxs uppercase tracking-wide text-neutral-6">
        {t('widgets.sidebars.node.replaceNodeControl.replacementNodeLabel')}
      </label>
      <select
        value={targetId}
        onChange={(e) => setTargetId(e.target.value)}
        className={selectClassName}
        aria-label={t(
          'widgets.sidebars.node.replaceNodeControl.replacementNodeLabel',
        )}
      >
        <option value="" disabled>
          {t('widgets.sidebars.node.replaceNodeControl.selectNode')}
        </option>
        {targets.map((n) => (
          <option key={n.id} value={n.id}>
            {targetLabel(n)}
          </option>
        ))}
      </select>

      <label className="text-xxs uppercase tracking-wide text-neutral-6 mt-1">
        {t('widgets.sidebars.node.replaceNodeControl.playbookLabel')}
      </label>
      <select
        value={templateId}
        onChange={(e) => setTemplateId(e.target.value)}
        className={selectClassName}
        aria-label={t(
          'widgets.sidebars.node.replaceNodeControl.playbookAriaLabel',
        )}
      >
        <option value="">
          {t('widgets.sidebars.node.replaceNodeControl.noneJustTransfer')}
        </option>
        {availableTemplates.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
      </select>

      <div className="flex items-center gap-2 mt-1">
        <Button
          size="md"
          kind="secondary"
          onClick={() => setConfirmOpen(true)}
          disabled={!targetId || busy}
        >
          {busy
            ? t('widgets.sidebars.node.replaceNodeControl.replacing')
            : t('widgets.sidebars.node.replaceNodeControl.replace')}
        </Button>
        {!embedded && (
          <button
            type="button"
            onClick={() => setOpen(false)}
            className="text-xs text-neutral-6 hover:text-neutral-3"
          >
            {t('widgets.sidebars.node.replaceNodeControl.cancel')}
          </button>
        )}
      </div>

      <ConfirmDialog
        open={confirmOpen}
        title={t('widgets.sidebars.node.replaceNodeControl.confirmTitle')}
        message={t('widgets.sidebars.node.replaceNodeControl.confirmMessage', {
          target: target ? targetLabel(target) : '',
          playbookSuffix: selectedTemplate
            ? t('widgets.sidebars.node.replaceNodeControl.confirmRunSuffix', {
                name: selectedTemplate.name,
              })
            : '',
        })}
        confirmText={t('widgets.sidebars.node.replaceNodeControl.replace')}
        destructive
        busy={busy}
        onConfirm={handleReplace}
        onCancel={() => setConfirmOpen(false)}
      />
    </div>
  )
}
