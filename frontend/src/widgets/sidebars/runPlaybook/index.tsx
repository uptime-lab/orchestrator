import { useNavigate } from '@tanstack/react-router'
import { FC, useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SubViewHeader } from '@/widgets/sidebars/node/subViewHeader'
import { SidebarEnum } from '@/widgets/sidebars/types'

import { nodesHooks } from '@/entities/node'
import { useOUs } from '@/entities/ou'
import { apiWorkflows } from '@/entities/workflow/api'
import {
  isWorkflowActive,
  isWorkflowAvailableForOU,
} from '@/entities/workflow/lib/scope'
import { useWorkflowTemplates } from '@/entities/workflow/model'
import {
  RunConsequencesNotice,
  useRunConsequences,
} from '@/entities/workflow/ui/runPlaybookDialog/RunConsequencesNotice'

import { Button } from '@/shared/ui/button/button.tsx'
import { extractApiErrorMessage } from '@/shared/utils/apiError'

import { SidebarProps } from '../types'

// SidebarRunPlaybook is the "extra menu" opened from the node Actions tab: it
// lists the playbooks whose scope covers this node and runs the selected one
// against it. Mirrors the Install OS sidebar shape (pick from a list → submit)
// so the two provisioning-style entries feel the same.
export const SidebarRunPlaybook: FC<
  SidebarProps & { nodeId: string; onDone?: () => void }
> = ({ nodeId, onDone }) => {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { node } = nodesHooks.useNode({ nodeId })
  const { templates, fetchTemplates } = useWorkflowTemplates()
  const { ous } = useOUs()
  const [selected, setSelected] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const online = node?.status === 'online'

  useEffect(() => {
    if (templates.length === 0) {
      fetchTemplates()
    }
  }, [templates.length, fetchTemplates])

  const handleBack = useCallback(() => {
    if (onDone) {
      onDone()
      return
    }
    navigate({
      to: '.',
      search: (old) => ({
        ...old,
        sidebar: SidebarEnum.Node,
        nodeId,
        view: 'actions',
      }),
    })
  }, [navigate, nodeId, onDone])

  // Only offer active playbooks whose scope covers this node's OU.
  const inScope = templates.filter((t) =>
    isWorkflowAvailableForOU(t, node?.ou_id, ous),
  )
  const availableTemplates = inScope.filter(isWorkflowActive)
  // Drafts in scope are the usual reason this list is empty, and the message
  // used to blame the absence of a playbook instead. Every creation path —
  // import, starter, clone — forces `draft`, so the common journey is: pick a
  // starter, save, come here, be told to go and create a playbook.
  const draftsInScope = inScope.length - availableTemplates.length

  // The same consequences and acknowledgement rule as a run started from the
  // Playbooks page.
  const selectedTemplate =
    availableTemplates.find((tpl) => tpl.id === selected) ?? null
  const { consequences, gate, acknowledged, setAcknowledged } =
    useRunConsequences(selectedTemplate)

  const handleSubmit = useCallback(
    async (event: React.FormEvent<HTMLFormElement>) => {
      event.preventDefault()
      setError(null)
      if (!selected) {
        setError(t('widgets.sidebars.runPlaybook.selectRequired'))
        return
      }
      if (gate !== 'ready') return
      setBusy(true)
      try {
        const execution = await apiWorkflows.postStartWorkflowExecution({
          template_id: selected,
          node_ids: [nodeId],
        })
        navigate({
          to: '.',
          search: {
            sidebar: SidebarEnum.WorkflowExecutionDetails,
            executionId: execution.id,
          },
        })
      } catch (err) {
        setError(
          extractApiErrorMessage(
            err,
            t('widgets.sidebars.runPlaybook.startError'),
          ),
        )
      } finally {
        setBusy(false)
      }
    },
    [selected, gate, nodeId, navigate, t],
  )

  return (
    <div className="flex flex-col gap-6">
      <SubViewHeader
        title={t('widgets.sidebars.runPlaybook.title')}
        onBack={handleBack}
      />

      {error && (
        <div className="bg-red-6/10 border border-red-6/50 text-red-6 px-4 py-2 rounded text-sm">
          {error}
        </div>
      )}

      {availableTemplates.length === 0 ? (
        <div className="text-sm text-neutral-6">
          {draftsInScope > 0 ? (
            <>
              {t('widgets.sidebars.runPlaybook.draftsInScope', {
                count: draftsInScope,
              })}{' '}
              {t('widgets.sidebars.runPlaybook.draftsHint')}
            </>
          ) : (
            <>{t('widgets.sidebars.runPlaybook.noPlaybooks')}</>
          )}
        </div>
      ) : (
        <form onSubmit={handleSubmit} className="flex flex-col gap-6">
          <div className="flex flex-col gap-2">
            <label className="text-sm font-medium text-neutral-7 uppercase">
              {t('widgets.sidebars.runPlaybook.playbookLabel')}
            </label>
            <select
              value={selected}
              onChange={(e) => setSelected(e.target.value)}
              className="px-3 py-2 bg-neutral-13 border border-neutral-11 rounded text-sm text-neutral-2 focus:outline-none focus:border-purple-5"
              required
            >
              <option value="">
                {t('widgets.sidebars.runPlaybook.selectPlaceholder')}
              </option>
              {availableTemplates.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </div>

          {selectedTemplate && (
            <RunConsequencesNotice
              consequences={consequences}
              gate={gate}
              acknowledged={acknowledged}
              onAcknowledgedChange={setAcknowledged}
              disabled={busy}
            />
          )}

          {!online && (
            <div className="text-sm text-neutral-6">
              {t('widgets.sidebars.runPlaybook.mustBeOnline')}
            </div>
          )}

          <Button
            type="submit"
            size="lg"
            disabled={busy || !selected || !online || gate !== 'ready'}
            grow
          >
            {busy
              ? t('widgets.sidebars.runPlaybook.starting')
              : t('widgets.sidebars.runPlaybook.submit')}
          </Button>
        </form>
      )}
    </div>
  )
}
